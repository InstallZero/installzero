// InstallZero eval console. Repos (with recipes and facts), datasets,
// campaigns that expand into queued jobs, and the scheduler that hands jobs
// to workers on the Windows host (Start-EvalWorker.ps1), which poll /api/worker/*.
//
// Scheduling: WSL distros share one network namespace, so jobs whose
// snapshots listen on the same ports never run together, a snapshot whose
// ports are not yet known runs alone, and only one Docker-based install runs
// at a time. Local models share one GPU (EVAL_GPU_LIMIT). One build at a
// time. Drafts, diagnoses and lesson drafts need no distro and always fit.
//
// Loss loop: each probe run's transcript is stored with its result; a
// diagnose job gives every missed fact a cause (probe loss or benchmark bug);
// a lessons job turns recurring probe losses on tuning datasets into a
// reviewable lesson set, which campaigns pass to the probe as guidance
// (prompt_version probe-N+<set id>). Held-out datasets never feed lessons.

const express = require('express');
const { MongoClient } = require('mongodb');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const PORT = Number(process.env.PORT || 8300);
const MONGO_URL = process.env.MONGO_URL || 'mongodb://root:example@mongo:27017/';
const DB_NAME = process.env.DATABASE_NAME || 'installzero';
const GPU_LIMIT = Number(process.env.EVAL_GPU_LIMIT || 1);
const WORKER_TIMEOUT_MS = 120000;
const HOSTED_MODELS = (process.env.EVAL_HOSTED_MODELS ||
  'anthropic:claude-opus-5-5,openai:gpt-5.6-sol,xai:grok-4.7,google:gemini-3.1-pro-preview').split(',').map(s => s.trim()).filter(Boolean);
const BASE_PORT_FROM = 8100, BASE_PORT_TO = 9990;
// Blocks holding a port many projects use by default are never handed out.
const COMMON_PORTS = [8080, 8443, 8888, 9000, 9090, 9200, 9300];
// Bind-mounted from console\models.json so models added here reach the repo.
const MODELS_FILE = process.env.EVAL_MODELS_FILE || path.join(__dirname, 'models.json');

let db;
const col = name => db.collection(name);
const repos = () => col('bench_repos'), datasets = () => col('bench_datasets'), jobs = () => col('bench_jobs'),
  campaigns = () => col('bench_campaigns'), results = () => col('bench_results'), workers = () => col('bench_workers'),
  snippets = () => col('bench_snippets'), transcripts = () => col('bench_transcripts'), lessons = () => col('bench_lessons'),
  savedModels = () => col('bench_models'), sftSets = () => col('bench_sft_sets'), trainRuns = () => col('bench_train_runs');

const now = () => new Date();
const newId = prefix => `${prefix}-${Date.now().toString(36)}${crypto.randomBytes(3).toString('hex')}`;
const slug = s => String(s).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40);
const sha1 = s => crypto.createHash('sha1').update(s, 'utf8').digest('hex');
// hf:<file>.gguf is a worker's local GGUF served on Hugging Face Jobs: no GPU slot here.
const isHostedModel = m => /^(anthropic|openai|xai|google):/.test(m);
const isLocalModel = m => !isHostedModel(m) && !/^hf:/.test(m);
// PowerShell's ConvertTo-Json sends a one-element array as a bare value.
const toPorts = v => (v == null ? [] : Array.isArray(v) ? v : [v]).map(Number).filter(Boolean);
const arr = v => v == null ? [] : Array.isArray(v) ? v : [v];
const ACTIVE = ['claimed', 'running'];
const PROBE_CAUSES = ['not_explored', 'out_of_budget', 'found_not_written'], BENCH_CAUSES = ['written_unmatched', 'not_observable'];
const NO_DISTRO = ['draft', 'diagnose', 'lessons', 'sft-build', 'train'];
// Probe engines: ps is Probe.ps1, go the Go port (installzero probe).
const ENGINES = ['ps', 'go'];

const clip = (s, n) => { s = s == null ? '' : typeof s === 'string' ? s : JSON.stringify(s); return s.length > n ? `${s.slice(0, n)} ...[${s.length - n} more chars]` : s; };
// AgentLoop.ps1 transcript (JSON lines) -> compact events. Two 'start'
// events mean two probes wrote one file (same-second run ids): unusable.
function compactTranscript(text) {
  const events = []; let starts = 0;
  for (const line of String(text || '').split(/\r?\n/)) {
    if (!line.trim()) continue;
    let e; try { e = JSON.parse(line.replace(/^\uFEFF/, '')); } catch { continue; }
    if (e.type === 'start') { starts++; events.push({ t: 'start', model: e.model, system: clip(e.system, 30000), user: clip(e.user, 4000) }); }
    else if (e.type === 'assistant') events.push({ t: 'assistant', text: clip(e.content, 4000), reasoning: clip(e.reasoning, 3000),
      calls: arr(e.tool_calls).map(c => ({ name: c.function ? c.function.name : c.name, args: clip(c.function ? c.function.arguments : c.arguments, 2000) })) });
    else if (e.type === 'tool') events.push({ t: 'tool', name: e.name, args: clip(e.arguments, 2000), out: clip(e.output, 8000) });
    else if (e.type === 'server_error') events.push({ t: 'error', text: clip(e.error, 1000) });
    else if (e.type === 'result') events.push({ t: 'result', run: e.run });
  }
  return { events, starts };
}
async function storeTranscript(resultId, text) {
  const { events, starts } = compactTranscript(text);
  if (!events.length) return 'empty';
  if (starts > 1) { await results().updateOne({ _id: resultId }, { $set: { transcript_mixed: true } }); return 'mixed'; }
  await transcripts().replaceOne({ _id: resultId }, { _id: resultId, events, created_at: now() }, { upsert: true });
  await results().updateOne({ _id: resultId }, { $set: { has_transcript: true } });
  return 'ok';
}
// Fact regexes are written for .NET; JavaScript has no leading inline flags.
function jsRegex(re) { try { return new RegExp(String(re).replace(/^\(\?[imsx]+\)/, ''), 'im'); } catch { return null; } }
async function repoOfResult(r) { return (r.bench_repo && await repos().findOne({ _id: r.bench_repo })) || await repos().findOne({ bench_id: r.repo }) || hostRepo(r.repo); }
// Runs from Invoke-ProbeEval.ps1 probed a real install on the Windows host;
// their repos and facts are in agent\eval\repos.json (mounted read-only).
const HOST_REPOS_FILE = process.env.EVAL_HOST_REPOS_FILE || path.join(__dirname, 'host-repos.json');
function hostRepo(id) {
  let list = [];
  try { list = arr(JSON.parse(fs.readFileSync(HOST_REPOS_FILE, 'utf8').replace(/^\uFEFF/, '')).repos); } catch { return null; }
  const e = list.find(x => x && x.id === id);
  if (!e) return null;
  return { _id: e.id, bench_id: e.id, host: true, facts: arr(e.facts), serves: e.serves || '', recipe: null,
    install_notes: `Not a WSL snapshot: this probe ran on the Windows host where the project was really installed (no recipe; the install was done by hand or by its installer). ${e.why || ''}` };
}
const guidanceText = L => (L.items || []).filter(i => i.accepted && i.kind === 'prompt').map(i => `- ${i.text}`).join('\n');

const app = express();
app.use(express.json({ limit: '20mb' }));
app.use((req, res, next) => { res.set('Cache-Control', 'no-store'); next(); });
app.use(express.static(path.join(__dirname, 'public')));
const wrap = fn => (req, res) => Promise.resolve(fn(req, res)).catch(e => { console.error(req.path, e); res.status(500).json({ error: String(e.message || e) }); });

// ---- repos ---------------------------------------------------------------------

function runnable(r) { return !!((r.has_recipe ?? (r.recipe && r.recipe.script)) && r.facts && r.facts.length && r.snapshot && r.snapshot.status === 'ready'); }
function repoStage(r) {
  if (r.snapshot && r.snapshot.status === 'ready') return 'ready';
  if (r.snapshot && r.snapshot.status === 'building') return 'building';
  if (r.snapshot && r.snapshot.status === 'failed') return 'build-failed';
  if (r.snapshot && r.snapshot.status === 'unhealthy') return 'unhealthy';
  if (r.has_recipe ?? (r.recipe && r.recipe.script)) return 'recipe';
  if (r.draft && r.draft.status === 'drafting') return 'drafting';
  if (r.has_draft ?? (r.draft && r.draft.recipe)) return 'draft';
  return 'metadata';
}

app.get('/api/repos', wrap(async (req, res) => {
  const list = await repos().aggregate([
    { $addFields: { has_recipe: { $gt: [{ $strLenCP: { $ifNull: ['$recipe.script', ''] } }, 0] }, has_draft: { $ne: [{ $ifNull: ['$draft.recipe', null] }, null] } } },
    { $unset: ['recipe', 'draft.recipe', 'draft.facts', 'snapshot.log_tail'] },
  ]).toArray();
  res.json(list.map(r => ({
    id: r._id, bench_id: r.bench_id || null, url: r.url, category: r.category, status: r.status, os: r.os, variant: r.variant,
    difficulty: r.rubric && r.rubric.difficulty, stars: r.github && r.github.stars, description: r.github && r.github.description,
    facts: (r.facts || []).length, stage: repoStage(r), runnable: runnable(r),
    snapshot: r.snapshot ? { status: r.snapshot.status, size_gb: r.snapshot.size_gb, built_at: r.snapshot.built_at, ports: r.snapshot.ports_known || r.snapshot.ports ? toPorts(r.snapshot.ports) : null } : null,
    disk_gb: r.disk_gb, updated_at: r.updated_at
  })));
}));

app.get('/api/repo', wrap(async (req, res) => {
  const r = await repos().findOne({ _id: req.query.id });
  if (!r) return res.status(404).json({ error: 'no such repo' });
  r.stage = repoStage(r);
  res.json(r);
}));

// Add a repo from a GitHub URL or owner/name: metadata from the GitHub API.
app.post('/api/repos', wrap(async (req, res) => {
  const m = String(req.body.url || '').trim().replace(/\.git$/, '').match(/(?:github\.com\/)?([\w.-]+)\/([\w.-]+)\/?$/);
  if (!m) return res.status(400).json({ error: 'give a GitHub URL or owner/name' });
  const id = `${m[1]}/${m[2]}`;
  const gh = await fetchGitHub(id);
  const existing = await repos().findOne({ _id: id });
  const benchId = existing && existing.bench_id ? existing.bench_id : await uniqueBenchId(slug(m[2]));
  const set = { url: `https://github.com/${id}`, github: gh, bench_id: benchId, updated_at: now() };
  if (req.body.category) set.category = req.body.category;
  await repos().updateOne({ _id: id }, { $set: set, $setOnInsert: { status: 'candidate', source: 'ui', created_at: now() } }, { upsert: true });
  res.json(await repos().findOne({ _id: id }));
}));

async function uniqueBenchId(base) {
  let id = base || 'repo', n = 2;
  while (await repos().findOne({ bench_id: id })) id = `${base}-${n++}`;
  return id;
}

async function fetchGitHub(id) {
  const headers = { 'User-Agent': 'installzero-evals', Accept: 'application/vnd.github+json' };
  if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
  const r = await fetch(`https://api.github.com/repos/${id}`, { headers });
  if (!r.ok) throw new Error(`GitHub ${r.status} for ${id}`);
  const j = await r.json();
  let release = null;
  const rr = await fetch(`https://api.github.com/repos/${id}/releases/latest`, { headers });
  if (rr.ok) release = (await rr.json()).tag_name || null;
  return { stars: j.stargazers_count, description: j.description, language: j.language, license: j.license && j.license.spdx_id,
    default_branch: j.default_branch, pushed_at: j.pushed_at, archived: j.archived, topics: j.topics, latest_release: release, fetched_at: now() };
}

// Edit recipe, facts, category, status or notes. A changed recipe makes the snapshot stale.
app.put('/api/repo', wrap(async (req, res) => {
  const id = req.query.id, b = req.body, set = { updated_at: now() };
  for (const k of ['category', 'status', 'install_notes', 'os', 'variant']) if (k in b) set[k] = b[k];
  if (b.facts) set.facts = validateFacts(b.facts);
  if (b.accept_draft) set['draft.accepted_at'] = now();
  if (b.recipe) {
    set.recipe = normalizeRecipe(b.recipe);
    const old = await repos().findOne({ _id: id }, { projection: { recipe: 1, snapshot: 1 } });
    if (old && old.snapshot && old.recipe && old.recipe.script !== set.recipe.script) set['snapshot.stale'] = true;
  }
  if (b.bench_id) {
    const clash = await repos().findOne({ bench_id: b.bench_id, _id: { $ne: id } });
    if (clash) return res.status(400).json({ error: `bench_id ${b.bench_id} is used by ${clash._id}` });
    set.bench_id = b.bench_id;
  }
  await repos().updateOne({ _id: id }, { $set: set });
  res.json(await repos().findOne({ _id: id }));
}));

// Accept the agent's draft as the recipe and facts.
app.post('/api/repo/accept-draft', wrap(async (req, res) => {
  const r = await repos().findOne({ _id: req.body.id });
  if (!r || !r.draft || !r.draft.recipe) return res.status(400).json({ error: 'no draft to accept' });
  await repos().updateOne({ _id: r._id }, { $set: { recipe: normalizeRecipe(r.draft.recipe), facts: validateFacts(r.draft.facts || []), 'draft.accepted_at': now(), updated_at: now() } });
  res.json(await repos().findOne({ _id: r._id }));
}));

app.delete('/api/repo', wrap(async (req, res) => { await repos().deleteOne({ _id: req.query.id }); res.json({ ok: true }); }));

function usesDocker(recipe) { return !!recipe && ((recipe.include || []).includes('_docker.sh') || /\bdocker\b/.test(recipe.script || '')); }
function normalizeRecipe(x) {
  return { script: String(x.script || ''), uses_docker: usesDocker(x), target: String(x.target || '/opt'), ready: String(x.ready || 'true'),
    ready_timeout_sec: Number(x.ready_timeout_sec || 300), include: (x.include || []).filter(Boolean), remove: (x.remove || []).filter(Boolean),
    ports_expected: toPorts(x.ports_expected) };
}
function validateFacts(facts) {
  return facts.map(f => {
    try { new RegExp(f.re, 'im'); } catch (e) { throw new Error(`fact ${f.id}: bad regex: ${e.message}`); }
    return { id: String(f.id), category: String(f.category || 'config'), weight: Number(f.weight || 1), re: String(f.re) };
  });
}

app.get('/api/snippets', wrap(async (req, res) => res.json(await snippets().find().toArray())));
app.put('/api/snippet', wrap(async (req, res) => {
  await snippets().updateOne({ _id: req.body.id }, { $set: { script: String(req.body.script || ''), updated_at: now() } }, { upsert: true });
  res.json({ ok: true });
}));

// Bulk import of recipes and facts (Import-BenchRecipes.ps1).
app.post('/api/import', wrap(async (req, res) => {
  let n = 0;
  for (const s of req.body.snippets || []) { await snippets().updateOne({ _id: s.id }, { $set: { script: s.script, updated_at: now() } }, { upsert: true }); }
  for (const r of req.body.repos || []) {
    const set = { bench_id: r.bench_id, recipe: normalizeRecipe(r.recipe), facts: validateFacts(r.facts || []), updated_at: now() };
    if (r.serves) set.serves = r.serves;
    if (r.why) set.install_notes = r.why;
    await repos().updateOne({ _id: r.id }, { $set: set, $setOnInsert: { url: `https://github.com/${r.id}`, status: 'selected', created_at: now() } }, { upsert: true });
    n++;
  }
  res.json({ imported: n });
}));

// Everything needed to write the recipe files back to git (Export-BenchRecipes.ps1).
app.get('/api/export', wrap(async (req, res) => {
  const list = await repos().find({ 'recipe.script': { $exists: true } }).toArray();
  res.json({ snippets: await snippets().find().toArray(), repos: list.map(r => ({ id: r._id, bench_id: r.bench_id, recipe: r.recipe, facts: r.facts, serves: r.serves, why: r.install_notes })) });
}));

// ---- datasets ------------------------------------------------------------------

app.get('/api/datasets', wrap(async (req, res) => {
  const list = await datasets().find().sort({ created_at: -1 }).toArray();
  const all = await repos().find({}, { projection: { bench_id: 1, facts: 1, snapshot: 1, recipe: 1 } }).toArray();
  const byId = new Map(all.map(r => [r._id, r]));
  res.json(list.map(d => ({ ...d, runnable: d.repos.filter(id => { const r = byId.get(id); return r && r.snapshot && r.snapshot.status === 'ready' && (r.facts || []).length; }).length })));
}));
app.post('/api/datasets', wrap(async (req, res) => {
  const name = String(req.body.name || '').trim();
  if (!name) return res.status(400).json({ error: 'name required' });
  const id = slug(name) || newId('ds');
  if (await datasets().findOne({ _id: id })) return res.status(400).json({ error: `dataset ${id} exists` });
  const d = { _id: id, name, description: req.body.description || '', repos: [...new Set(req.body.repos || [])], frozen: false, created_at: now(), updated_at: now() };
  await datasets().insertOne(d);
  res.json(d);
}));
app.put('/api/dataset', wrap(async (req, res) => {
  const d = await datasets().findOne({ _id: req.query.id });
  if (!d) return res.status(404).json({ error: 'no such dataset' });
  const set = { updated_at: now() };
  if ('frozen' in req.body) set.frozen = !!req.body.frozen;
  if ('role' in req.body) set.role = ['tuning', 'heldout'].includes(req.body.role) ? req.body.role : '';
  if (!d.frozen || req.body.frozen === false) {
    for (const k of ['name', 'description']) if (k in req.body) set[k] = req.body[k];
    if (req.body.repos) set.repos = [...new Set(req.body.repos)];
    if (req.body.add) set.repos = [...new Set([...d.repos, ...req.body.add])];
    if (req.body.remove) set.repos = (set.repos || d.repos).filter(x => !req.body.remove.includes(x));
  } else if (req.body.repos || req.body.add || req.body.remove) return res.status(400).json({ error: 'dataset is frozen' });
  await datasets().updateOne({ _id: d._id }, { $set: set });
  res.json(await datasets().findOne({ _id: d._id }));
}));
app.delete('/api/dataset', wrap(async (req, res) => { await datasets().deleteOne({ _id: req.query.id }); res.json({ ok: true }); }));

// ---- campaigns and jobs --------------------------------------------------------

// Saved models live in bench_models and MODELS_FILE: the file's entries are
// loaded at start, new ones (from campaigns, or used before this existed) are
// added to both. Never removes; edit the file and restart to drop one.
async function saveModels(ids, source, campaign = null) {
  const have = new Set((await savedModels().find({}, { projection: { _id: 1 } }).toArray()).map(m => m._id));
  let order = await savedModels().countDocuments(), added = 0;
  for (const id of [...new Set(ids.map(m => String(m || '').trim()).filter(Boolean))]) {
    if (have.has(id)) continue;
    await savedModels().insertOne({ _id: id, source, campaign, added_at: now(), order: order++ });
    have.add(id); added++;
  }
  if (added) writeModelsFile();
  return added;
}

async function writeModelsFile() {
  const all = await savedModels().find().sort({ order: 1, _id: 1 }).toArray();
  let doc = {};
  try { doc = JSON.parse(fs.readFileSync(MODELS_FILE, 'utf8')); } catch { }
  doc.models = all.map(m => ({ id: m._id, added: m.added_at.toISOString().slice(0, 10), source: m.source }));
  const text = JSON.stringify(doc, null, 2).replace(/\{\n\s+("id": [^\n]+)\n\s+("added": [^\n]+)\n\s+("source": [^\n]+)\n\s+\}/g, '{ $1 $2 $3 }') + '\n';
  // Written in place: a bind-mounted single file cannot be replaced by a rename.
  try { fs.writeFileSync(MODELS_FILE, text); } catch (e) { console.error(`cannot write ${MODELS_FILE}: ${e.message}`); }
}

async function loadModels() {
  let file = [];
  try { file = arr(JSON.parse(fs.readFileSync(MODELS_FILE, 'utf8')).models); }
  catch (e) { console.error(`cannot read ${MODELS_FILE}: ${e.message}`); }
  const have = new Set((await savedModels().find({}, { projection: { _id: 1 } }).toArray()).map(m => m._id));
  let order = have.size;
  for (const m of file) {
    if (!m || !m.id || have.has(m.id)) continue;
    await savedModels().insertOne({ _id: m.id, source: m.source || 'file', campaign: null, added_at: m.added ? new Date(m.added) : now(), order: order++ });
    have.add(m.id);
  }
  const used = [...await campaigns().distinct('models'), ...(await results().distinct('model')).map(m => String(m || '').replace(/ \([\w-]+\)$/, ''))];
  if (!await saveModels(used, 'history')) await writeModelsFile();
}

app.get('/api/models', wrap(async (req, res) => {
  const ws = await workers().find({ last_seen: { $gt: new Date(Date.now() - WORKER_TIMEOUT_MS) } }).toArray();
  const local = [...new Set(ws.flatMap(w => w.local_models || []))];
  const saved = (await savedModels().find().sort({ order: 1, _id: 1 }).toArray()).map(m => m._id);
  res.json({ saved, local, hosted: saved.filter(isHostedModel), hf: local.filter(m => /\.gguf$/i.test(m)).map(m => `hf:${m}`) });
}));

app.get('/api/campaigns', wrap(async (req, res) => {
  const list = await campaigns().find().sort({ created_at: -1 }).limit(100).toArray();
  const counts = await jobs().aggregate([{ $match: { campaign: { $in: list.map(c => c._id) } } }, { $group: { _id: { c: '$campaign', s: '$status' }, n: { $sum: 1 } } }]).toArray();
  for (const c of list) { c.counts = {}; for (const x of counts) if (x._id.c === c._id) c.counts[x._id.s] = x.n; }
  res.json(list);
}));

// dataset x models x runs -> probe jobs for the repos with a ready snapshot.
app.post('/api/campaigns', wrap(async (req, res) => {
  const b = req.body, d = await datasets().findOne({ _id: b.dataset });
  if (!d) return res.status(400).json({ error: 'pick a dataset' });
  const models = (b.models || []).filter(Boolean), runs = Math.max(1, Number(b.runs || 1));
  if (!models.length) return res.status(400).json({ error: 'pick at least one model' });
  const engines = [...new Set(arr(b.engines).filter(Boolean))];
  if (!engines.length) engines.push('ps');
  if (engines.some(e => !ENGINES.includes(e))) return res.status(400).json({ error: `engines are ${ENGINES.join(' and ')}` });
  const list = await repos().find({ _id: { $in: d.repos } }).toArray();
  const ok = list.filter(r => r.snapshot && r.snapshot.status === 'ready' && (r.facts || []).length && r.recipe);
  const skipped = d.repos.filter(id => !ok.find(r => r._id === id));
  let L = null;
  if (b.guidance) {
    L = await lessons().findOne({ _id: b.guidance });
    if (!L || L.status !== 'active') return res.status(400).json({ error: 'guidance must be an active lesson set' });
  }
  const id = newId('cmp');
  const c = { _id: id, name: b.name || `${d.name} - ${new Date().toISOString().slice(0, 16).replace('T', ' ')}`, dataset: d._id, models, engines, runs,
    priority: Number(b.priority || 0), max_turns: Number(b.max_turns || 40), skipped, guidance: L ? L._id : null,
    diagnose: !!b.diagnose, diagnose_model: b.diagnose_model || HOSTED_MODELS[0], created_at: now() };
  await campaigns().insertOne(c);
  const docs = [];
  for (const r of ok) for (const m of models) for (const e of engines) for (let i = 1; i <= runs; i++) docs.push(newJob('probe', r, { model: m, engine: e, campaign: id, dataset: d._id, run: i, priority: c.priority, max_turns: c.max_turns, guidance: c.guidance }));
  if (docs.length) await jobs().insertMany(docs);
  await saveModels(models, 'campaign', id);
  const tuned = L ? ok.filter(r => (L.source.repos || []).includes(r.bench_id)).length : 0;
  res.json({ campaign: c, jobs: docs.length, skipped, tuned_on: tuned });
}));

function newJob(type, r, extra = {}) {
  return { _id: newId(type), type, repo: r._id, bench_id: r.bench_id, status: 'queued', priority: 0, attempts: 0,
    gpu: type === 'train' || (type === 'probe' && extra.model ? isLocalModel(extra.model) : false), created_at: now(), log: [], ...extra };
}

// Single jobs: build a snapshot, draft a recipe, or one probe run.
app.post('/api/jobs', wrap(async (req, res) => {
  const b = req.body, r = await repos().findOne({ _id: b.repo });
  if (!r) return res.status(400).json({ error: 'no such repo' });
  if (!r.bench_id) { r.bench_id = await uniqueBenchId(slug(r._id.split('/')[1])); await repos().updateOne({ _id: r._id }, { $set: { bench_id: r.bench_id } }); }
  if (b.type === 'build' && !(r.recipe && r.recipe.script)) return res.status(400).json({ error: 'no recipe yet: draft one and accept it, or write one' });
  if (b.type === 'probe' && !(r.snapshot && r.snapshot.status === 'ready')) return res.status(400).json({ error: 'no healthy snapshot yet: build it first' });
  if (b.type === 'measure' && !(r.snapshot && ['ready', 'unhealthy'].includes(r.snapshot.status))) return res.status(400).json({ error: 'no snapshot yet: build it first' });
  if (!['build', 'draft', 'probe', 'measure'].includes(b.type)) return res.status(400).json({ error: 'type is build, draft, probe or measure' });
  const dup = await jobs().findOne({ repo: r._id, type: b.type, status: { $in: ['queued', ...ACTIVE] }, ...(b.type === 'probe' ? { model: b.model } : {}) });
  if (dup && b.type !== 'probe') return res.status(400).json({ error: `a ${b.type} job for this repo is already ${dup.status}` });
  const extra = { priority: Number(b.priority || 0) };
  if (b.type === 'probe' && b.engine && !ENGINES.includes(b.engine)) return res.status(400).json({ error: `engine is ${ENGINES.join(' or ')}` });
  if (b.type === 'probe') Object.assign(extra, { model: b.model, engine: b.engine || 'ps', max_turns: Number(b.max_turns || 40), run: 1 });
  if (b.type === 'draft') Object.assign(extra, { model: b.model || HOSTED_MODELS[0], feedback: b.feedback || '' });
  const j = newJob(b.type, r, extra);
  await jobs().insertOne(j);
  if (b.type === 'draft') await repos().updateOne({ _id: r._id }, { $set: { 'draft.status': 'drafting', 'draft.job': j._id } });
  if (b.type === 'build') await repos().updateOne({ _id: r._id }, { $set: { 'snapshot.status': 'queued', 'snapshot.job': j._id } });
  res.json(j);
}));

app.get('/api/jobs', wrap(async (req, res) => {
  const q = {};
  if (req.query.status) q.status = { $in: String(req.query.status).split(',') };
  if (req.query.campaign) q.campaign = req.query.campaign;
  if (req.query.type) q.type = req.query.type;
  const text = String(req.query.q || '').trim().slice(0, 200);
  if (text) {
    const re = new RegExp(text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'i');
    q.$or = ['bench_id', 'model', 'worker', 'error', '_id', 'lesson_set'].map(f => ({ [f]: re }));
  }
  const finite = v => { const n = Number(v); return Number.isFinite(n) ? n : 0; };
  const limit = Math.min(200, Math.max(1, finite(req.query.limit) || 50));
  const total = await jobs().countDocuments(q);
  const pages = Math.max(1, Math.ceil(total / limit));
  const page = Math.min(Math.max(1, finite(req.query.page) || 1), pages);
  const list = await jobs().find(q, { projection: { log: 0, spec: 0 } }).sort({ created_at: -1 }).skip((page - 1) * limit).limit(limit).toArray();
  res.json({ jobs: list, total, page, pages, limit });
}));
app.get('/api/job', wrap(async (req, res) => {
  const j = await jobs().findOne({ _id: req.query.id });
  if (!j) return res.status(404).json({ error: 'no such job' });
  res.json(j);
}));

app.post('/api/job/action', wrap(async (req, res) => {
  const { id, action } = req.body, j = await jobs().findOne({ _id: id });
  if (!j) return res.status(404).json({ error: 'no such job' });
  if (action === 'cancel') {
    if (j.status === 'queued') await jobs().updateOne({ _id: id }, { $set: { status: 'cancelled', finished_at: now() } });
    else if (ACTIVE.includes(j.status)) await jobs().updateOne({ _id: id }, { $set: { cancel_requested: true } });
  } else if (action === 'retry') {
    await jobs().updateOne({ _id: id }, { $set: { status: 'queued', worker: null, error: null, cancel_requested: false, progress: null, log: [] }, $unset: { started_at: 1, finished_at: 1 } });
  } else if (action === 'delete') {
    if (ACTIVE.includes(j.status)) return res.status(400).json({ error: 'cancel it first' });
    await jobs().deleteOne({ _id: id });
  } else if (action === 'priority') {
    await jobs().updateOne({ _id: id }, { $set: { priority: Number(req.body.priority || 0) } });
  } else return res.status(400).json({ error: 'action is cancel, retry, delete or priority' });
  res.json({ ok: true });
}));

app.post('/api/campaign/action', wrap(async (req, res) => {
  const { id, action } = req.body;
  if (action === 'cancel') {
    await jobs().updateMany({ campaign: id, status: 'queued' }, { $set: { status: 'cancelled', finished_at: now() } });
    await jobs().updateMany({ campaign: id, status: { $in: ACTIVE } }, { $set: { cancel_requested: true } });
  } else if (action === 'retry-failed') {
    await jobs().updateMany({ campaign: id, status: { $in: ['failed', 'cancelled'] } }, { $set: { status: 'queued', worker: null, error: null, cancel_requested: false, progress: null, log: [] } });
  } else if (action === 'delete') {
    await jobs().deleteMany({ campaign: id, status: { $nin: ACTIVE } });
    if (!(await jobs().countDocuments({ campaign: id }))) await campaigns().deleteOne({ _id: id });
  } else return res.status(400).json({ error: 'action is cancel, retry-failed or delete' });
  res.json({ ok: true });
}));

// ---- workers and the scheduler -----------------------------------------------------

app.get('/api/workers', wrap(async (req, res) => {
  const ws = await workers().find().sort({ last_seen: -1 }).toArray();
  res.json(ws.map(w => ({ ...w, online: w.last_seen > new Date(Date.now() - WORKER_TIMEOUT_MS) })));
}));

app.post('/api/worker/heartbeat', wrap(async (req, res) => {
  const b = req.body;
  await workers().updateOne({ _id: b.worker }, { $set: { host: b.host, slots: b.slots, gpu_slots: b.gpu_slots, running: b.running || [], local_models: b.local_models || [],
    snapshot_root: b.snapshot_root, last_seen: now() } }, { upsert: true });
  // Snapshots on the worker's disk are the truth for what can run. One whose
  // ports were never recorded gets a measure job, so it stops running alone.
  for (const s of b.snapshots || []) {
    const r = await repos().findOne({ bench_id: s.bench_id }, { projection: { snapshot: 1, bench_id: 1, recipe: 1 } });
    if (!r) continue;
    const cur = r.snapshot || {};
    if (cur.status === 'building' || cur.status === 'queued' || cur.status === 'unhealthy') continue;
    const set = { 'snapshot.status': 'ready', 'snapshot.size_gb': s.size_gb, 'snapshot.built_at': s.built_at ? new Date(s.built_at) : cur.built_at, 'snapshot.worker': b.worker };
    if (s.measured) { set['snapshot.ports'] = toPorts(s.ports); set['snapshot.ports_known'] = true; }
    await repos().updateOne({ _id: r._id }, { $set: set });
    if (!s.measured && !cur.ports_known && r.recipe && !(await jobs().findOne({ repo: r._id, type: 'measure', status: { $in: ['queued', ...ACTIVE] } }))) {
      await jobs().insertOne(newJob('measure', r, { priority: 100 }));
    }
  }
  // Jobs this worker holds but no longer runs (it restarted) go back to the queue.
  const orphans = await jobs().find({ worker: b.worker, status: { $in: ACTIVE }, _id: { $nin: b.running || [] }, claimed_at: { $lt: new Date(Date.now() - 30000) } }).toArray();
  for (const j of orphans) await requeueOrFail(j, `worker ${b.worker} restarted`);
  const cancel = (await jobs().find({ _id: { $in: b.running || [] }, cancel_requested: true }, { projection: { _id: 1 } }).toArray()).map(j => j._id);
  res.json({ cancel });
}));

let claiming = Promise.resolve();
app.post('/api/worker/claim', wrap(async (req, res) => {
  const run = claiming.then(() => claimJob(req.body));
  claiming = run.catch(() => {});
  res.json({ job: await run });
}));

// WSL distros share one network namespace: jobs may not share a listening
// port, and only one Docker install may run at a time (Docker daemons in one
// namespace clobber each other's bridges and iptables rules).
function portsOf(job, byId) {
  if (NO_DISTRO.includes(job.type)) return { wsl: false, ports: [], docker: false };
  const r = byId.get(job.repo);
  const known = r && r.snapshot && (r.snapshot.ports_known || toPorts(r.snapshot.ports).length) ? toPorts(r.snapshot.ports) : null;
  const expected = r && r.recipe && toPorts(r.recipe.ports_expected).length ? toPorts(r.recipe.ports_expected) : null;
  return { wsl: true, ports: job.type === 'build' ? (expected || known) : (known || expected), docker: !!(r && r.recipe && r.recipe.uses_docker) };
}

async function claimJob({ worker, free_slots, gpu_free, types }) {
  if (!free_slots) return null;
  const active = await jobs().find({ status: { $in: ACTIVE } }).toArray();
  const q = { status: 'queued' };
  if (types && types.length) q.type = { $in: types };
  const queued = await jobs().find(q).sort({ priority: -1, created_at: 1 }).limit(300).toArray();
  const ids = [...new Set([...active, ...queued].map(j => j.repo))];
  const byId = new Map((await repos().find({ _id: { $in: ids } }, { projection: { 'snapshot.status': 1, 'snapshot.ports': 1, 'snapshot.ports_known': 1, 'recipe.ports_expected': 1, 'recipe.uses_docker': 1 } }).toArray()).map(r => [r._id, r]));
  const busy = new Set(), busyRepos = new Set();
  let exclusive = false, gpuBusy = 0, buildBusy = 0, wslBusy = 0, dockerBusy = false;
  for (const j of active) {
    const p = portsOf(j, byId);
    if (p.wsl) { wslBusy++; busyRepos.add(j.repo); if (p.ports) p.ports.forEach(x => busy.add(x)); else exclusive = true; }
    if (p.docker) dockerBusy = true;
    if (j.gpu) gpuBusy++;
    if (j.type === 'build') buildBusy++;
  }
  for (const j of queued) {
    const p = portsOf(j, byId);
    if (j.gpu && (gpuBusy >= GPU_LIMIT || !gpu_free)) continue;
    if (j.type === 'build' && buildBusy) continue;
    if (j.type === 'probe' && ((byId.get(j.repo) || {}).snapshot || {}).status === 'unhealthy') continue;
    if (p.docker && dockerBusy) continue;
    if (p.wsl) {
      if (exclusive || busyRepos.has(j.repo)) continue;
      if (!p.ports && wslBusy) continue;
      if (p.ports && p.ports.some(x => busy.has(x))) continue;
    }
    const spec = await jobSpec(j);
    const got = await jobs().findOneAndUpdate({ _id: j._id, status: 'queued' },
      { $set: { status: 'claimed', worker, claimed_at: now(), ports: p.ports, spec }, $inc: { attempts: 1 } }, { returnDocument: 'after' });
    if (got) {
      if (j.type === 'build') await repos().updateOne({ _id: j.repo }, { $set: { 'snapshot.status': 'building', 'snapshot.job': j._id } });
      return got;
    }
  }
  return null;
}

// What the worker needs to run the job without reading MongoDB itself.
async function jobSpec(j) {
  if (j.type === 'lessons') return lessonsSpec(j);
  if (j.type === 'diagnose') return diagnoseSpec(j);
  if (j.type === 'sft-build') return sftBuildSpec(j);
  if (j.type === 'train') return trainSpec(j);
  const r = await repos().findOne({ _id: j.repo });
  const spec = { repo: r._id, bench_id: r.bench_id, url: r.url, recipe: r.recipe || null, facts: r.facts || [], serves: r.serves || '', why: r.install_notes || '' };
  if (j.type === 'probe' && j.guidance) {
    const L = await lessons().findOne({ _id: j.guidance });
    if (L) spec.guidance = { id: L._id, text: guidanceText(L) };
  }
  if (r.recipe && r.recipe.include && r.recipe.include.length) spec.snippets = await snippets().find({ _id: { $in: r.recipe.include } }).toArray();
  if (j.type === 'draft') {
    spec.github = r.github || null; spec.category = r.category || ''; spec.feedback = j.feedback || '';
    spec.base_port = (r.draft && r.draft.base_port) || await freeBasePort(r._id);
    await repos().updateOne({ _id: r._id }, { $set: { 'draft.base_port': spec.base_port } });
    spec.previous = r.recipe ? { recipe: r.recipe, facts: r.facts } : (r.draft && r.draft.recipe ? { recipe: r.draft.recipe, facts: r.draft.facts } : null);
    spec.build_error = r.snapshot && r.snapshot.status === 'failed' ? { error: r.snapshot.error, log_tail: r.snapshot.log_tail } : null;
  }
  return spec;
}

// One run: missed facts with their regexes, the document, the transcript and
// the recipe (ground truth). seen_in_outputs: the regex matches raw tool
// output, a hint that the probe saw it (regexes target document wording).
async function diagnoseSpec(j) {
  const res = await results().findOne({ _id: j.result_id });
  if (!res) throw new Error(`no result ${j.result_id}`);
  const r = await repoOfResult(res);
  const t = await transcripts().findOne({ _id: res._id });
  const events = t ? t.events.filter(e => e.t !== 'result') : [];
  const outputs = events.filter(e => e.t === 'tool').map(e => e.out).join('\n');
  const missIds = new Set(arr(res.misses).map(m => m.id)), facts = (r && r.facts) || [];
  return {
    repo: r ? r._id : res.repo, bench_id: res.repo, serves: (r && r.serves) || '', why: (r && r.install_notes) || '',
    recipe: r && r.recipe ? { script: r.recipe.script, target: r.recipe.target, remove: r.recipe.remove || [] } : null,
    result: { id: res._id, model: res.model, prompt_version: res.prompt_version, recall: res.recall, stop_reason: res.stop_reason,
      turns: res.probe && res.probe.stats ? res.probe.stats.model_turns : null, gaps: res.probe ? res.probe.gaps : '', install_md_text: res.install_md_text || '' },
    missed: facts.filter(f => missIds.has(f.id)).map(f => { const re = jsRegex(f.re); return { ...f, seen_in_outputs: !!(re && re.test(outputs)) }; }),
    hit: facts.filter(f => !missIds.has(f.id)).map(f => f.id),
    transcript: events,
  };
}

const lessonQuery = L => ({ delivered: true, diagnosis: { $exists: true }, repo: { $in: L.source.repos || [] }, ...(L.source.models && L.source.models.length ? { model: { $in: L.source.models } } : {}) });
async function lessonsSpec(j) {
  const L = await lessons().findOne({ _id: j.lesson_set });
  if (!L) throw new Error(`no lesson set ${j.lesson_set}`);
  const rows = await results().find(lessonQuery(L), { projection: { repo: 1, model: 1, misses: 1, diagnosis: 1 } }).toArray();
  const misses = [], habits = [];
  for (const r of rows) {
    const byId = new Map(arr(r.misses).map(m => [m.id, m]));
    for (const m of arr(r.diagnosis.misses)) {
      const f = byId.get(m.id) || {};
      misses.push({ repo: r.repo, model: String(r.model || '').replace(/^local:/, ''), fact: m.id, category: f.category || '', weight: f.weight || 1, cause: m.cause, lesson: m.lesson || '', evidence: clip(m.evidence, 240) });
    }
    for (const h of arr(r.diagnosis.habits)) habits.push({ repo: r.repo, text: h });
  }
  const parent = L.parent ? await lessons().findOne({ _id: L.parent }) : null;
  const latest = await transcripts().findOne({ 'events.0.t': 'start' }, { sort: { created_at: -1 }, projection: { events: { $slice: 1 } } });
  return { lesson_set: L._id, runs: rows.length, repos: [...new Set(rows.map(r => r.repo))], misses, habits,
    parent_items: parent ? (parent.items || []).filter(i => i.accepted).map(i => ({ text: i.text, kind: i.kind })) : [],
    probe_prompt: latest && latest.events[0] ? clip(latest.events[0].system, 15000) : '' };
}

async function queueDiagnose(resultId, model) {
  if (await jobs().findOne({ type: 'diagnose', result_id: resultId, status: { $in: ['queued', ...ACTIVE] } })) return null;
  const res = await results().findOne({ _id: resultId }, { projection: { repo: 1, bench_repo: 1 } });
  const r = res && await repoOfResult(res);
  if (!r) return null;
  const j = newJob('diagnose', r, { result_id: resultId, model: model || HOSTED_MODELS[0], priority: 10 });
  await jobs().insertOne(j);
  return j;
}

// A block of ten ports no other recipe or snapshot uses, for a new recipe.
async function freeBasePort(exceptId) {
  const all = await repos().find({ _id: { $ne: exceptId } }, { projection: { snapshot: 1, recipe: 1, draft: 1 } }).toArray();
  const used = new Set();
  for (const r of all) {
    for (const p of [...toPorts(r.snapshot && r.snapshot.ports), ...toPorts(r.recipe && r.recipe.ports_expected), ...toPorts(r.draft && r.draft.recipe && r.draft.recipe.ports_expected)]) used.add(p);
    // Reserved when a draft starts, so drafts running together get different blocks.
    if (r.draft && r.draft.base_port) for (let p = r.draft.base_port; p < r.draft.base_port + 10; p++) used.add(p);
  }
  COMMON_PORTS.forEach(p => used.add(p));
  for (let b = BASE_PORT_FROM; b <= BASE_PORT_TO; b += 10) { let free = true; for (let p = b; p < b + 10; p++) if (used.has(p)) { free = false; break; } if (free) return b; }
  return BASE_PORT_TO;
}

app.post('/api/worker/progress', wrap(async (req, res) => {
  const b = req.body, set = { last_progress: now() };
  if (b.status === 'running') { set.status = 'running'; set.started_at = b.started_at ? new Date(b.started_at) : now(); }
  if (b.progress) set.progress = b.progress;
  if (b.distro) set.distro = b.distro;
  const upd = { $set: set };
  if (b.log && b.log.length) upd.$push = { log: { $each: b.log.map(l => String(l).slice(0, 2000)), $slice: -600 } };
  await jobs().updateOne({ _id: b.id, status: { $in: ACTIVE } }, upd);
  const j = await jobs().findOne({ _id: b.id }, { projection: { cancel_requested: 1 } });
  res.json({ cancel: !!(j && j.cancel_requested) });
}));

app.post('/api/worker/complete', wrap(async (req, res) => {
  const b = req.body, j = await jobs().findOne({ _id: b.id });
  if (!j) return res.status(404).json({ error: 'no such job' });
  const status = j.cancel_requested && b.status !== 'done' ? 'cancelled' : b.status;
  const set = { status, finished_at: now(), error: b.error || null };
  if (b.progress) set.progress = b.progress;
  if (b.log && b.log.length) await jobs().updateOne({ _id: j._id }, { $push: { log: { $each: b.log, $slice: -600 } } });
  if (j.type === 'probe' && b.result) {
    const row = b.result, key = `${row.repo}|${row.model}|${row.time}|${row.install_md}`;
    const L = j.guidance ? await lessons().findOne({ _id: j.guidance }, { projection: { 'source.repos': 1 } }) : null;
    const doc = { ...row, _id: sha1(key), run_at: new Date(row.time), job_id: j._id, campaign: j.campaign || null, dataset: j.dataset || null,
      bench_repo: j.repo, provider: /^(\w+):/.test(row.model || '') ? row.model.split(':')[0] : 'local', imported_at: now(),
      guidance: j.guidance || null, tuned_on: !!(L && (L.source.repos || []).includes(row.repo)), transcript_path: row.transcript || null };
    delete doc.time; delete doc.transcript;
    await results().replaceOne({ _id: doc._id }, doc, { upsert: true });
    set.result_id = doc._id; set.recall = row.recall; set.delivered = row.delivered;
    if (b.transcript) await storeTranscript(doc._id, b.transcript);
    const c = j.campaign ? await campaigns().findOne({ _id: j.campaign }, { projection: { diagnose: 1, diagnose_model: 1 } }) : null;
    if (c && c.diagnose && row.delivered && arr(row.misses).length && b.transcript) await queueDiagnose(doc._id, c.diagnose_model);
  }
  if (j.type === 'diagnose') {
    if (status === 'done' && b.diagnosis) {
      const d = b.diagnosis, causes = [...PROBE_CAUSES, ...BENCH_CAUSES];
      await results().updateOne({ _id: j.result_id }, { $set: { diagnosis: {
        model: j.model, at: now(), job: j._id, summary: clip(d.summary, 800), habits: arr(d.habits).map(h => clip(h, 400)).slice(0, 5), tokens: d.tokens || null,
        misses: arr(d.misses).map(m => ({ id: String(m.id), cause: causes.includes(m.cause) ? m.cause : 'unclear', evidence: clip(m.evidence, 600),
          lesson: clip(m.lesson, 400), fact_fix: clip(m.fact_fix, 400), seen_in_outputs: !!m.seen_in_outputs })),
      } } });
    }
  }
  if (j.type === 'lessons') {
    if (status === 'done' && b.lessons) {
      const items = arr(b.lessons.items).map((x, i) => {
        const rs = [...new Set(arr(x.repos).map(String))], kind = x.kind === 'tool' ? 'tool' : 'prompt';
        return { id: `i${i + 1}`, text: String(x.text || ''), kind, categories: arr(x.categories).map(String), repos: rs, rationale: String(x.rationale || ''), weak: rs.length < 2, accepted: kind === 'prompt' && rs.length >= 2 };
      });
      await lessons().updateOne({ _id: j.lesson_set }, { $set: { status: 'draft', items, notes: String(b.lessons.notes || ''), tokens: b.lessons.tokens || null, drafted_at: now() } });
    } else await lessons().updateOne({ _id: j.lesson_set }, { $set: { status: 'failed', error: b.error || status } });
  }
  if (j.type === 'sft-build') {
    if (status === 'done' && b.sft) {
      const { samples, ...manifest } = b.sft;
      await sftSets().updateOne({ _id: j.sft_set, job: j._id }, { $set: { manifest, samples: arr(samples), built_at: now(), worker: j.worker }, $unset: { error: '' } });
    } else await sftSets().updateOne({ _id: j.sft_set, job: j._id }, { $set: { error: b.error || status } });
  }
  if (j.type === 'train') {
    if (status === 'done' && b.train) await trainRuns().updateOne({ _id: j.train_run, job: j._id }, { $set: { output: b.train, finished_at: now(), worker: j.worker }, $unset: { error: '' } });
    else await trainRuns().updateOne({ _id: j.train_run, job: j._id }, { $set: { error: b.error || status } });
  }
  if ((j.type === 'probe' || j.type === 'measure') && status === 'done' && 'ports' in b) {
    await repos().updateOne({ _id: j.repo, 'snapshot.ports_known': { $ne: true } }, { $set: { 'snapshot.ports': toPorts(b.ports), 'snapshot.ports_known': true } });
  }
  if (j.type === 'measure' && status === 'done') await repos().updateOne({ _id: j.repo, 'snapshot.status': 'unhealthy' }, { $set: { 'snapshot.status': 'ready' }, $unset: { 'snapshot.error': '' } });
  if (j.type === 'measure' && status === 'failed') await repos().updateOne({ _id: j.repo, 'snapshot.status': 'ready' }, { $set: { 'snapshot.status': 'unhealthy', 'snapshot.error': b.error || 'measure failed' } });
  if (j.type === 'build') {
    const snap = status === 'done'
      ? { status: 'ready', built_at: now(), size_gb: b.snapshot && b.snapshot.size_gb, ports: toPorts(b.snapshot && b.snapshot.ports), ports_known: true, worker: j.worker, job: j._id, stale: false, log_tail: (b.snapshot && b.snapshot.log_tail) || '' }
      : { status: 'failed', error: b.error || status, log_tail: (b.snapshot && b.snapshot.log_tail) || '', job: j._id, worker: j.worker };
    await repos().updateOne({ _id: j.repo }, { $set: { snapshot: snap, updated_at: now() } });
  }
  if (j.type === 'draft') {
    const d = status === 'done' && b.draft ? { status: 'ready', ...b.draft, model: j.model, job: j._id, drafted_at: now() } : { status: 'failed', error: b.error || status, job: j._id };
    const cur = await repos().findOne({ _id: j.repo }, { projection: { 'draft.base_port': 1 } });
    if (cur && cur.draft && cur.draft.base_port) d.base_port = cur.draft.base_port;
    await repos().updateOne({ _id: j.repo }, { $set: { draft: d, updated_at: now() } });
    if (status === 'done' && b.draft && b.draft.rubric) await repos().updateOne({ _id: j.repo, rubric: { $exists: false } }, { $set: { rubric: b.draft.rubric } });
  }
  await jobs().updateOne({ _id: j._id }, { $set: set });
  res.json({ ok: true });
}));

// Jobs of a worker that went silent go back to the queue (twice), then fail.
async function reap() {
  try {
    const dead = (await workers().find({ last_seen: { $lt: new Date(Date.now() - WORKER_TIMEOUT_MS) } }).toArray()).map(w => w._id);
    if (!dead.length) return;
    const stuck = await jobs().find({ status: { $in: ACTIVE }, worker: { $in: dead } }).toArray();
    for (const j of stuck) await requeueOrFail(j, `worker ${j.worker} stopped responding`);
  } catch (e) { console.error('reap', e); }
}

async function requeueOrFail(j, why) {
  const retry = j.attempts < 3 && !j.cancel_requested;
  const set = retry ? { status: 'queued', worker: null, progress: null } : { status: j.cancel_requested ? 'cancelled' : 'failed', error: why, finished_at: now() };
  await jobs().updateOne({ _id: j._id, status: { $in: ACTIVE } }, { $set: set, $push: { log: `[server] ${why}; ${retry ? 'requeued' : 'gave up'}` } });
  if (j.type === 'build') await repos().updateOne({ _id: j.repo }, { $set: { 'snapshot.status': retry ? 'queued' : 'failed' } });
  if (j.type === 'lessons' && !retry) await lessons().updateOne({ _id: j.lesson_set }, { $set: { status: 'failed', error: why } });
  if (j.type === 'sft-build' && !retry) await sftSets().updateOne({ _id: j.sft_set, job: j._id }, { $set: { error: why } });
  if (j.type === 'train' && !retry) await trainRuns().updateOne({ _id: j.train_run, job: j._id }, { $set: { error: why } });
}

// ---- results and analysis ------------------------------------------------------------

const benchIdsOf = async repoIds => (await repos().find({ _id: { $in: repoIds } }, { projection: { bench_id: 1 } }).toArray()).map(r => r.bench_id).filter(Boolean);
async function resultQuery(p) {
  const q = { delivered: { $exists: true } };
  if (p.ids) q._id = { $in: arr(p.ids) };
  if (p.campaign) q.campaign = p.campaign;
  if (p.models) q.model = { $in: Array.isArray(p.models) ? p.models : String(p.models).split(',') };
  if (p.prompt_version) q.prompt_version = p.prompt_version;
  // Rows from before the Go engine have no engine: they are ps.
  if (p.engine) q.engine = p.engine === 'ps' ? { $in: [null, 'ps'] } : p.engine;
  if (p.dataset) { const d = await datasets().findOne({ _id: p.dataset }); if (d) q.repo = { $in: await benchIdsOf(d.repos) }; }
  if (p.target_env === 'wsl') q.target_env = /^wsl/;
  else if (p.target_env) q.target_env = p.target_env;
  if (p.exclude_tuned === '1' || p.exclude_tuned === true) q.tuned_on = { $ne: true };
  return q;
}

app.get('/api/results', wrap(async (req, res) => {
  const rows = await results().find(await resultQuery(req.query), { projection: { install_md_text: 0, 'probe.gaps': 0 } }).sort({ run_at: -1 }).limit(Number(req.query.limit || 5000)).toArray();
  const pending = new Set(await jobs().distinct('result_id', { type: 'diagnose', status: { $in: ['queued', ...ACTIVE] } }));
  for (const r of rows) if (pending.has(r._id)) r.diagnosing = true;
  res.json(rows);
}));
app.get('/api/result', wrap(async (req, res) => res.json(await results().findOne({ _id: req.query.id }))));

// ---- transcripts, diagnoses and lessons ---------------------------------------------

app.get('/api/transcript', wrap(async (req, res) => res.json(await transcripts().findOne({ _id: req.query.id }))));
// Results whose transcript is still only on the host (Push-Transcripts.ps1).
app.get('/api/transcripts/missing', wrap(async (req, res) => {
  res.json(await results().aggregate([
    { $match: { has_transcript: { $ne: true }, transcript_mixed: { $ne: true } } },
    { $project: { install_md: 1, transcript_path: 1, has_install_md: { $gt: [{ $strLenCP: { $ifNull: ['$install_md_text', ''] } }, 0] } } },
  ]).toArray());
}));
app.post('/api/transcript', wrap(async (req, res) => {
  const b = req.body;
  if (!(await results().findOne({ _id: b.id }, { projection: { _id: 1 } }))) return res.status(404).json({ error: 'no such result' });
  if (b.install_md) await results().updateOne({ _id: b.id, install_md_text: { $exists: false } }, { $set: { install_md_text: String(b.install_md) } });
  res.json({ status: b.jsonl ? await storeTranscript(b.id, b.jsonl) : 'empty' });
}));

// Queue diagnoses for scored runs with misses and a transcript (not yet diagnosed, unless force).
app.post('/api/diagnose', wrap(async (req, res) => {
  const b = req.body, q = { ...(await resultQuery(b)), delivered: true, 'misses.0': { $exists: true }, has_transcript: true };
  if (!b.force) q.diagnosis = { $exists: false };
  const ids = (await results().find(q, { projection: { _id: 1 } }).limit(2000).toArray()).map(r => r._id);
  const pending = new Set(await jobs().distinct('result_id', { type: 'diagnose', status: { $in: ['queued', ...ACTIVE] }, result_id: { $in: ids } }));
  let n = 0; for (const id of ids) if (!pending.has(id) && await queueDiagnose(id, b.model)) n++;
  res.json({ queued: n, matched: ids.length, already_queued: pending.size, no_facts: ids.length - pending.size - n });
}));

app.get('/api/lessons', wrap(async (req, res) => res.json(await lessons().find().sort({ created_at: -1 }).toArray())));
app.get('/api/lesson', wrap(async (req, res) => {
  const L = await lessons().findOne({ _id: req.query.id });
  if (!L) return res.status(404).json({ error: 'no such lesson set' });
  res.json({ ...L, guidance_text: guidanceText(L), used_by: await campaigns().countDocuments({ guidance: L._id }) });
}));

// Draft a lesson set from the diagnosed runs on tuning datasets.
app.post('/api/lessons', wrap(async (req, res) => {
  const b = req.body, ds = await datasets().find({ _id: { $in: arr(b.datasets) } }).toArray();
  if (!ds.length) return res.status(400).json({ error: 'pick at least one dataset' });
  const held = ds.filter(d => d.role === 'heldout');
  if (held.length) return res.status(400).json({ error: `held-out datasets cannot feed lessons: ${held.map(d => d.name).join(', ')}` });
  const n = (await lessons().countDocuments()) + 1;
  let id = `L${n}`; for (let k = n + 1; await lessons().findOne({ _id: id }); k++) id = `L${k}`;
  const L = { _id: id, name: String(b.name || '').trim() || id, status: 'drafting', items: [], parent: b.parent || null, model: b.model || HOSTED_MODELS[0],
    source: { datasets: ds.map(d => d._id), models: arr(b.models).filter(Boolean), repos: await benchIdsOf([...new Set(ds.flatMap(d => d.repos))]) }, created_at: now() };
  L.source.results = await results().countDocuments(lessonQuery(L));
  if (!L.source.results) return res.status(400).json({ error: 'no diagnosed runs on these datasets yet: diagnose them first (Losses tab)' });
  await lessons().insertOne(L);
  const j = newJob('lessons', { _id: null, bench_id: null }, { lesson_set: id, model: L.model, priority: 10 });
  await jobs().insertOne(j);
  await lessons().updateOne({ _id: id }, { $set: { job: j._id } });
  res.json({ ...L, job: j._id });
}));

app.put('/api/lesson', wrap(async (req, res) => {
  const L = await lessons().findOne({ _id: req.query.id });
  if (!L) return res.status(404).json({ error: 'no such lesson set' });
  if (L.status !== 'draft') return res.status(400).json({ error: `only drafts can be edited (this one is ${L.status}); fork it` });
  const set = { updated_at: now() };
  if ('name' in req.body) set.name = String(req.body.name || L._id);
  if (req.body.items) set.items = arr(req.body.items).map((x, i) => ({ id: x.id || `i${i + 1}`, text: String(x.text || '').trim(), kind: x.kind === 'tool' ? 'tool' : 'prompt',
    categories: arr(x.categories).map(String), repos: arr(x.repos).map(String), rationale: String(x.rationale || ''), weak: arr(x.repos).length < 2, accepted: !!x.accepted })).filter(x => x.text);
  await lessons().updateOne({ _id: L._id }, { $set: set });
  res.json(await lessons().findOne({ _id: L._id }));
}));

// activate: frozen, usable as campaign guidance. fork: an editable copy. redraft: rerun the lessons job.
app.post('/api/lesson/action', wrap(async (req, res) => {
  const { id, action } = req.body, L = await lessons().findOne({ _id: id });
  if (!L) return res.status(404).json({ error: 'no such lesson set' });
  if (action === 'activate') {
    if (L.status !== 'draft') return res.status(400).json({ error: `only drafts can be activated (this one is ${L.status})` });
    if (!guidanceText(L)) return res.status(400).json({ error: 'accept at least one prompt item first' });
    await lessons().updateOne({ _id: id }, { $set: { status: 'active', activated_at: now() } });
  } else if (action === 'retire') {
    await lessons().updateOne({ _id: id }, { $set: { status: 'retired', retired_at: now() } });
  } else if (action === 'fork') {
    const n = (await lessons().countDocuments()) + 1;
    let nid = `L${n}`; for (let k = n + 1; await lessons().findOne({ _id: nid }); k++) nid = `L${k}`;
    const F = { ...L, _id: nid, name: `${L.name} (fork)`, status: 'draft', parent: L._id, created_at: now() };
    for (const k of ['activated_at', 'retired_at', 'job', 'error', 'updated_at']) delete F[k];
    await lessons().insertOne(F);
    return res.json(F);
  } else if (action === 'redraft') {
    if (await jobs().findOne({ type: 'lessons', lesson_set: id, status: { $in: ['queued', ...ACTIVE] } })) return res.status(400).json({ error: 'already drafting' });
    if (['active', 'retired'].includes(L.status)) return res.status(400).json({ error: 'fork it to redraft' });
    const j = newJob('lessons', { _id: null, bench_id: null }, { lesson_set: id, model: L.model, priority: 10 });
    await jobs().insertOne(j);
    await lessons().updateOne({ _id: id }, { $set: { status: 'drafting', job: j._id }, $unset: { error: '' } });
  } else if (action === 'delete') {
    if (await campaigns().countDocuments({ guidance: id })) return res.status(400).json({ error: 'campaigns used it; retire it instead' });
    await lessons().deleteOne({ _id: id });
  } else return res.status(400).json({ error: 'action is activate, retire, fork, redraft or delete' });
  res.json({ ok: true });
}));

// ---- SFT sets and training runs ------------------------------------------------------
//
// An SFT set is a frozen export of probe trajectories worth imitating
// (sft-build job, agent\eval\sft\Export-SftDataset.ps1 on the worker that has
// the raw transcripts). A training run fine-tunes a local base model on one
// with LoRA and drops the quantized GGUF into the doctor assets (train job,
// Invoke-SftTrain.ps1); the worker then reports it as a local model, so a
// campaign on the set's held-out repos scores it like any other model.

// Base models a train job can start from. The id is a Hugging Face repo; the
// form lets you type another.
const TRAIN_BASES = [
  { id: 'google/gemma-4-E4B-it', label: 'Gemma 4 E4B', prefix: 'gemma-4-E4B-it-iz', base_gguf: 'gemma-4-E4B-it-Q4_K_M.gguf', qlora: true, max_len: 16384, note: 'Fits a 16 GB GPU with QLoRA. Gated on Hugging Face: accept the licence and set HF_TOKEN on the worker.' },
  { id: 'Qwen/Qwen3.5-9B', label: 'Qwen 3.5 9B', prefix: 'Qwen3.5-9B-iz', base_gguf: 'Qwen3.5-9B-Q4_K_M.gguf', qlora: true, max_len: 12288, note: 'Dense 9B; QLoRA on 16 GB with shorter sequences.' },
  { id: 'ibm-granite/granite-4.1-8b', label: 'Granite 4.1 8B', prefix: 'granite-4.1-8b-iz', base_gguf: 'granite-4.1-8b-Q4_K_M.gguf', qlora: true, max_len: 12288, note: 'Dense 8B; QLoRA on 16 GB.' },
  { id: 'openai/gpt-oss-20b', label: 'gpt-oss-20b', prefix: 'gpt-oss-20b-iz', base_gguf: 'gpt-oss-20b-MXFP4.gguf', qlora: false, max_len: 16384, note: 'MoE 21B in MXFP4; LoRA needs the bf16 weights and well over 16 GB of VRAM. Run it on a worker with an 80 GB GPU.' },
];
const SFT_DEFAULTS = { min_recall: 0.8, prompt_version: 'probe-5', max_repeats: 3, val_fraction: 0.1, context_tokens: 32768 };
const TRAIN_DEFAULTS = { epochs: 2, lr: 1e-4, lora_r: 16, lora_alpha: 32, batch: 1, grad_accum: 16, max_examples: 0 };
const QUANTS = ['Q4_K_M', 'Q5_K_M', 'Q6_K', 'Q8_0'];
// Single-GPU Hugging Face Jobs flavors (train.py puts the model on one GPU),
// with list prices in USD/hour for the console's cost estimates.
const HF_FLAVORS = [
  { id: 'a100-large', label: 'A100 80 GB', vram: 80, price: 2.5 },
  { id: 'h200', label: 'H200 141 GB', vram: 141, price: 5 },
  { id: 'rtx-pro-6000', label: 'RTX PRO 6000 96 GB', vram: 96, price: 2.75 },
  { id: 'l40sx1', label: 'L40S 48 GB', vram: 48, price: 1.8 },
  { id: 'a10g-large', label: 'A10G 24 GB', vram: 24, price: 1.5 },
  { id: 'l4x1', label: 'L4 24 GB', vram: 24, price: 0.8 },
];
const HF_DEFAULTS = { flavor: 'a100-large', timeout_h: 6 };
const isHfRun = R => !!(R.compute && R.compute.target === 'hf');

const pvQuery = pv => ({ $regex: `^${String(pv).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(\\+|$)` });
const sftCandidateQuery = f => ({ delivered: true, recall: { $gte: Number(f.min_recall) }, prompt_version: pvQuery(f.prompt_version), target_env: /^wsl/ });
async function nextSeqId(coll, prefix) {
  const n = (await coll.countDocuments()) + 1;
  let id = `${prefix}-${n}`; for (let k = n + 1; await coll.findOne({ _id: id }); k++) id = `${prefix}-${k}`;
  return id;
}
// A set's or run's state follows its latest job, so retrying or cancelling from the queue shows here.
async function withJobs(docs, done, active) {
  const js = new Map((await jobs().find({ _id: { $in: docs.map(d => d.job).filter(Boolean) } }, { projection: { status: 1, progress: 1, worker: 1, error: 1, started_at: 1, finished_at: 1 } }).toArray()).map(j => [j._id, j]));
  return docs.map(d => {
    const j = js.get(d.job) || null;
    const status = !j ? (d.manifest || d.output ? done : 'failed') : j.status === 'done' ? done : ACTIVE.includes(j.status) ? active : j.status;
    return { ...d, status, job_info: j };
  });
}

async function sftBuildSpec(j) {
  const S = await sftSets().findOne({ _id: j.sft_set });
  if (!S) throw new Error(`no SFT set ${j.sft_set}`);
  const candidates = await results().find(sftCandidateQuery(S.filter), { projection: { repo: 1, model: 1, provider: 1, recall: 1, delivered: 1, prompt_version: 1, transcript_path: 1, target_env: 1, campaign: 1, run_at: 1 } }).toArray();
  return { set_id: S._id, filter: S.filter, heldout_repos: S.heldout_repos, val_fraction: S.val_fraction, context_tokens: S.context_tokens, include_reasoning: !!S.include_reasoning, candidates };
}
async function trainSpec(j) {
  const R = await trainRuns().findOne({ _id: j.train_run });
  if (!R) throw new Error(`no training run ${j.train_run}`);
  const S = await sftSets().findOne({ _id: R.sft_set });
  if (!S || !S.manifest) throw new Error(`SFT set ${R.sft_set} is not built`);
  return { run_id: R._id, base_model: R.base_model, set: { id: S._id, path: S.manifest.path, host: S.manifest.host }, params: R.params, quant: R.quant, output_name: R.output_name,
    compute: R.compute || { target: 'local' } };
}

app.get('/api/sft/options', wrap(async (req, res) => {
  const pvs = (await results().distinct('prompt_version', { delivered: true })).filter(Boolean);
  const base = [...new Set(pvs.map(v => String(v).split('+')[0]))].sort().reverse();
  res.json({ defaults: SFT_DEFAULTS, train_defaults: TRAIN_DEFAULTS, bases: TRAIN_BASES, quants: QUANTS, prompt_versions: base, hf_flavors: HF_FLAVORS, hf_defaults: HF_DEFAULTS,
    datasets: (await datasets().find({}, { projection: { name: 1, role: 1, repos: 1 } }).toArray()).map(d => ({ _id: d._id, name: d.name, role: d.role || '', repos: d.repos.length })) });
}));

// What a set with these settings would start from, before any transcript is read.
app.get('/api/sft/preview', wrap(async (req, res) => {
  const f = { ...SFT_DEFAULTS, ...req.query };
  const rows = await results().find(sftCandidateQuery(f), { projection: { repo: 1, model: 1, provider: 1 } }).toArray();
  const held = new Set(await benchIdsOf((await datasets().find({ _id: { $in: arr(req.query.heldout).flatMap(s => String(s).split(',')).filter(Boolean) } }).toArray()).flatMap(d => d.repos)));
  const byProvider = {};
  for (const r of rows) {
    const p = r.provider || (/^(\w+):/.test(r.model || '') ? r.model.split(':')[0] : 'local');
    const x = byProvider[p] = byProvider[p] || { provider: p, runs: 0, heldout_runs: 0, repos: new Set(), models: new Set() };
    x.runs++; if (held.has(r.repo)) x.heldout_runs++; x.repos.add(r.repo); x.models.add(r.model);
  }
  res.json({ candidates: rows.length, heldout_repos: held.size, providers: Object.values(byProvider).map(x => ({ ...x, repos: x.repos.size, models: [...x.models] })).sort((a, b) => b.runs - a.runs) });
}));

app.get('/api/sft/sets', wrap(async (req, res) => {
  const list = await sftSets().find({}, { projection: { samples: 0 } }).sort({ created_at: -1 }).toArray();
  const used = await trainRuns().aggregate([{ $group: { _id: '$sft_set', n: { $sum: 1 } } }]).toArray();
  const out = await withJobs(list, 'ready', 'building');
  for (const s of out) s.train_runs = (used.find(u => u._id === s._id) || {}).n || 0;
  res.json(out);
}));
app.get('/api/sft/set', wrap(async (req, res) => {
  const S = await sftSets().findOne({ _id: req.query.id });
  if (!S) return res.status(404).json({ error: 'no such SFT set' });
  res.json((await withJobs([S], 'ready', 'building'))[0]);
}));

app.post('/api/sft/sets', wrap(async (req, res) => {
  const b = req.body, num = (v, d) => (v === '' || v == null || !Number.isFinite(Number(v)) ? d : Number(v));
  const filter = { min_recall: num(b.min_recall, SFT_DEFAULTS.min_recall), prompt_version: String(b.prompt_version || SFT_DEFAULTS.prompt_version),
    max_repeats: num(b.max_repeats, SFT_DEFAULTS.max_repeats), providers: arr(b.providers).filter(Boolean), models: arr(b.models).filter(Boolean) };
  const heldDs = await datasets().find({ _id: { $in: arr(b.heldout_datasets).filter(Boolean) } }).toArray();
  const S = { _id: await nextSeqId(sftSets(), 'sft'), name: String(b.name || '').trim(), notes: String(b.notes || ''), filter,
    heldout_datasets: heldDs.map(d => d._id), heldout_repos: await benchIdsOf([...new Set(heldDs.flatMap(d => d.repos))]),
    val_fraction: Math.min(0.5, Math.max(0, num(b.val_fraction, SFT_DEFAULTS.val_fraction))), context_tokens: Math.max(4096, num(b.context_tokens, SFT_DEFAULTS.context_tokens)),
    include_reasoning: !!b.include_reasoning, created_at: now() };
  if (!S.name) S.name = S._id;
  if (!(await results().countDocuments(sftCandidateQuery(filter)))) return res.status(400).json({ error: 'no delivered WSL runs match: lower the recall or pick another prompt version' });
  const j = newJob('sft-build', { _id: null, bench_id: S._id }, { sft_set: S._id, priority: 20 });
  S.job = j._id;
  await sftSets().insertOne(S);
  await jobs().insertOne(j);
  res.json(S);
}));

app.post('/api/sft/set/action', wrap(async (req, res) => {
  const { id, action } = req.body, S = await sftSets().findOne({ _id: id });
  if (!S) return res.status(404).json({ error: 'no such SFT set' });
  const cur = S.job ? await jobs().findOne({ _id: S.job }) : null;
  if (action === 'rebuild') {
    if (cur && ['queued', ...ACTIVE].includes(cur.status)) return res.status(400).json({ error: `already ${cur.status}` });
    if (S.manifest && await trainRuns().countDocuments({ sft_set: id })) return res.status(400).json({ error: 'training runs used this set; create a new set instead so they stay traceable' });
    const j = newJob('sft-build', { _id: null, bench_id: S._id }, { sft_set: S._id, priority: 20 });
    await jobs().insertOne(j);
    await sftSets().updateOne({ _id: id }, { $set: { job: j._id }, $unset: { manifest: '', samples: '', error: '', built_at: '' } });
  } else if (action === 'delete') {
    if (await trainRuns().countDocuments({ sft_set: id })) return res.status(400).json({ error: 'training runs used this set; delete them first' });
    if (cur && ACTIVE.includes(cur.status)) return res.status(400).json({ error: 'cancel the build first' });
    if (cur && cur.status === 'queued') await jobs().updateOne({ _id: cur._id }, { $set: { status: 'cancelled', finished_at: now() } });
    await sftSets().deleteOne({ _id: id });
  } else return res.status(400).json({ error: 'action is rebuild or delete' });
  res.json({ ok: true });
}));

app.get('/api/train/runs', wrap(async (req, res) => {
  res.json(await withJobs(await trainRuns().find().sort({ created_at: -1 }).toArray(), 'done', 'training'));
}));

app.post('/api/train/runs', wrap(async (req, res) => {
  const b = req.body, S = await sftSets().findOne({ _id: b.sft_set });
  if (!S || !S.manifest) return res.status(400).json({ error: 'pick a built SFT set' });
  const baseModel = String(b.base_model || '').trim();
  if (!baseModel) return res.status(400).json({ error: 'pick a base model' });
  const known = TRAIN_BASES.find(x => x.id === baseModel);
  const num = (v, d) => (v === '' || v == null || !Number.isFinite(Number(v)) ? d : Number(v));
  let compute = { target: 'local' };
  if (b.compute === 'hf') {
    const fl = HF_FLAVORS.find(f => f.id === b.hf_flavor) || HF_FLAVORS.find(f => f.id === HF_DEFAULTS.flavor);
    compute = { target: 'hf', flavor: fl.id, price: fl.price, timeout_h: Math.min(48, Math.max(0.25, num(b.hf_timeout_h, HF_DEFAULTS.timeout_h))) };
  }
  const params = { epochs: num(b.epochs, TRAIN_DEFAULTS.epochs), lr: num(b.lr, TRAIN_DEFAULTS.lr), lora_r: num(b.lora_r, TRAIN_DEFAULTS.lora_r),
    lora_alpha: num(b.lora_alpha, TRAIN_DEFAULTS.lora_alpha), batch: num(b.batch, TRAIN_DEFAULTS.batch), grad_accum: num(b.grad_accum, TRAIN_DEFAULTS.grad_accum),
    max_len: num(b.max_len, known ? known.max_len : 16384), qlora: b.qlora == null ? (known && compute.target === 'local' ? known.qlora : compute.target === 'local') : !!b.qlora,
    max_examples: num(b.max_examples, 0) };
  const quant = QUANTS.includes(b.quant) ? b.quant : QUANTS[0];
  if (compute.target === 'hf' && !/^[\w.-]+\/[\w.-]+$/.test(baseModel)) return res.status(400).json({ error: 'Hugging Face Jobs need a Hub repo id (org/model) as the base' });
  const id = await nextSeqId(trainRuns(), 'ft');
  const R = { _id: id, name: String(b.name || '').trim() || id, base_model: baseModel, base_label: known ? known.label : baseModel, sft_set: S._id, params, quant, compute,
    output_name: `${known ? known.prefix : slug(baseModel.split('/').pop())}-${id}`, notes: String(b.notes || ''), created_at: now() };
  const j = newJob('train', { _id: null, bench_id: id }, { train_run: id, model: baseModel, priority: num(b.priority, 50), gpu: !isHfRun(R) });
  R.job = j._id;
  await trainRuns().insertOne(R);
  await jobs().insertOne(j);
  res.json(R);
}));

app.post('/api/train/run/action', wrap(async (req, res) => {
  const { id, action } = req.body, R = await trainRuns().findOne({ _id: id });
  if (!R) return res.status(404).json({ error: 'no such training run' });
  const cur = R.job ? await jobs().findOne({ _id: R.job }) : null;
  if (action === 'cancel') {
    if (!cur) return res.status(400).json({ error: 'no job' });
    if (cur.status === 'queued') await jobs().updateOne({ _id: cur._id }, { $set: { status: 'cancelled', finished_at: now() } });
    else if (ACTIVE.includes(cur.status)) await jobs().updateOne({ _id: cur._id }, { $set: { cancel_requested: true } });
  } else if (action === 'retry') {
    if (cur && ['queued', ...ACTIVE].includes(cur.status)) return res.status(400).json({ error: `already ${cur.status}` });
    const j = newJob('train', { _id: null, bench_id: id }, { train_run: id, model: R.base_model, priority: cur ? cur.priority : 50, gpu: !isHfRun(R) });
    await jobs().insertOne(j);
    await trainRuns().updateOne({ _id: id }, { $set: { job: j._id }, $unset: { output: '', error: '', finished_at: '' } });
  } else if (action === 'delete') {
    if (cur && ACTIVE.includes(cur.status)) return res.status(400).json({ error: 'cancel it first' });
    if (cur && cur.status === 'queued') await jobs().updateOne({ _id: cur._id }, { $set: { status: 'cancelled', finished_at: now() } });
    await trainRuns().deleteOne({ _id: id });
  } else return res.status(400).json({ error: 'action is cancel, retry or delete' });
  res.json({ ok: true });
}));

app.get('/api/summary', wrap(async (req, res) => {
  const byStatus = await jobs().aggregate([{ $group: { _id: '$status', n: { $sum: 1 } } }]).toArray();
  const ws = await workers().find({ last_seen: { $gt: new Date(Date.now() - WORKER_TIMEOUT_MS) } }).toArray();
  res.json({ jobs: Object.fromEntries(byStatus.map(x => [x._id, x.n])), workers: ws.length, slots: ws.reduce((a, w) => a + (w.slots || 0), 0),
    repos: await repos().countDocuments(), recipes: await repos().countDocuments({ 'recipe.script': { $exists: true, $ne: '' } }),
    snapshots: await repos().countDocuments({ 'snapshot.status': 'ready' }), datasets: await datasets().countDocuments(), results: await results().countDocuments() });
}));

app.get('*', (req, res) => res.sendFile(path.join(__dirname, 'public', 'index.html')));

async function main() {
  for (let i = 0; ; i++) {
    try { const c = await MongoClient.connect(MONGO_URL); db = c.db(DB_NAME); break; }
    catch (e) { console.error(`mongo connect failed (${e.message}); retrying`); await new Promise(r => setTimeout(r, Math.min(30000, 1000 * 2 ** i))); }
  }
  await jobs().createIndex({ status: 1, priority: -1, created_at: 1 });
  await jobs().createIndex({ campaign: 1 });
  await results().createIndex({ model: 1, repo: 1 });
  await jobs().createIndex({ type: 1, result_id: 1 });
  await repos().createIndex({ bench_id: 1 }, { unique: true, partialFilterExpression: { bench_id: { $type: 'string' } } });
  await loadModels();
  setInterval(reap, 30000);
  app.listen(PORT, () => console.log(`InstallZero evals on :${PORT} (db ${DB_NAME})`));
}
main();
