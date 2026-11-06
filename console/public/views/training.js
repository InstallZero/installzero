// Training: SFT sets exported from probe trajectories (which runs, which
// replies, how they split by repo) and LoRA fine-tunes of local base models
// on them. Both run as jobs in the queue (sft-build, train); a finished run's
// GGUF is a local model the New eval tab can score on the set's held-out repos.
UI.register('training', (() => {
  let opts = null, sets = [], runs = [], previewTimer = null, previewGen = 0;
  const num = id => document.getElementById(id).value;

  async function mount(root, params) {
    root.innerHTML = `
      <div class="grid grid-cols-1 xl:grid-cols-2 gap-4 mb-4">
        <div class="card"><h2>New SFT set</h2>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
            <div class="col-span-2"><label class="lbl">Name</label><input id="s-name" class="inp w-full" placeholder="defaults to the set id"></div>
            <div><label class="lbl">Prompt version</label><select id="s-pv" class="inp w-full"></select></div>
            <div><label class="lbl">Min recall</label><input id="s-recall" type="number" step="0.05" min="0" max="1" class="inp w-full"></div>
            <div><label class="lbl" title="Runs with this many repeated calls are dropped as looping">Max repeats</label><input id="s-rep" type="number" min="1" class="inp w-full"></div>
            <div><label class="lbl" title="Share of non-held-out repos used for validation, chosen by a stable hash of the repo name">Val fraction</label><input id="s-val" type="number" step="0.05" min="0" max="0.5" class="inp w-full"></div>
            <div class="col-span-2"><label class="lbl" title="The context the fine-tuned model will run with in the probe. Each example's history is trimmed the way the probe trims it for that context.">Student context (tokens)</label><input id="s-ctx" type="number" step="1024" min="4096" class="inp w-full"></div>
            <div class="col-span-2 md:col-span-4"><label class="lbl">Held-out datasets (their repos are never exported; score the fine-tune on them)</label><div id="s-held" class="flex flex-wrap gap-x-4 gap-y-1 text-sm"></div></div>
            <div class="col-span-2 md:col-span-4"><label class="lbl">Teacher providers (whose replies become training targets)</label><div id="s-prov" class="text-sm"></div></div>
            <div class="col-span-2 md:col-span-4"><label class="flex items-center gap-2 text-sm"><input id="s-reason" type="checkbox"> Keep the teachers' reasoning text in targets</label></div>
          </div>
          <div class="flex items-center gap-3 mt-3"><button id="s-go" class="btn btn-primary">Create and build</button><span id="s-est" class="text-xs text-slate-400"></span></div>
        </div>
        <div class="card"><h2>New training run</h2>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
            <div class="col-span-2"><label class="lbl">SFT set</label><select id="t-set" class="inp w-full"></select></div>
            <div class="col-span-2"><label class="lbl">Base model</label><select id="t-base" class="inp w-full"></select></div>
            <div class="col-span-2 md:col-span-4 hidden" id="t-other-wrap"><label class="lbl">Hugging Face repo or local folder</label><input id="t-other" class="inp w-full" placeholder="org/model"></div>
            <div class="col-span-2 md:col-span-4 text-xs text-slate-400" id="t-note"></div>
            <div class="col-span-2"><label class="lbl">Train on</label><select id="t-compute" class="inp w-full"><option value="local">This worker's GPU</option><option value="hf">Hugging Face Jobs (cloud GPU, billed per minute)</option></select></div>
            <div class="t-hf hidden"><label class="lbl">GPU</label><select id="t-flavor" class="inp w-full"></select></div>
            <div class="t-hf hidden"><label class="lbl" title="Hugging Face stops the job at this limit; you are billed at most this long">Time limit (h)</label><input id="t-timeout" type="number" step="0.5" min="0.25" max="48" class="inp w-full"></div>
            <div><label class="lbl">Epochs</label><input id="t-epochs" type="number" step="0.5" min="0.5" class="inp w-full"></div>
            <div><label class="lbl">Learning rate</label><input id="t-lr" class="inp w-full"></div>
            <div><label class="lbl">LoRA rank</label><input id="t-r" type="number" min="1" class="inp w-full"></div>
            <div><label class="lbl">LoRA alpha</label><input id="t-alpha" type="number" min="1" class="inp w-full"></div>
            <div><label class="lbl" title="Examples longer than this (in tokens of the base model's chat template) are dropped">Max length</label><input id="t-len" type="number" step="1024" class="inp w-full"></div>
            <div><label class="lbl">Grad. accumulation</label><input id="t-acc" type="number" min="1" class="inp w-full"></div>
            <div><label class="lbl">Quantization</label><select id="t-quant" class="inp w-full"></select></div>
            <div><label class="lbl" title="0 uses every train example; a few hundred makes a quick smoke test">Max examples</label><input id="t-max" type="number" min="0" class="inp w-full"></div>
            <div class="col-span-2 md:col-span-4 text-xs text-amber-300 hidden" id="t-fit"></div>
            <div class="col-span-2"><label class="lbl">Name</label><input id="t-name" class="inp w-full" placeholder="defaults to the run id"></div>
            <div><label class="lbl">Priority</label><input id="t-prio" type="number" value="50" class="inp w-full"></div>
            <div class="flex items-end"><label class="flex items-center gap-2 text-sm"><input id="t-qlora" type="checkbox"> 4-bit (QLoRA)</label></div>
          </div>
          <div class="flex items-center gap-3 mt-3"><button id="t-go" class="btn btn-primary">Queue training</button>
            <span class="text-xs text-slate-400" id="t-hint"></span></div>
        </div>
      </div>
      <div class="card mb-4"><h2>SFT sets</h2><div id="sets" class="text-sm"></div></div>
      <div class="card"><h2>Training runs</h2><div id="runs" class="text-sm"></div></div>`;

    opts = await UI.get('/api/sft/options');
    const d = opts.defaults, t = opts.train_defaults;
    document.getElementById('s-pv').innerHTML = opts.prompt_versions.map(v => `<option>${UI.esc(v)}</option>`).join('');
    document.getElementById('s-pv').value = opts.prompt_versions.includes(d.prompt_version) ? d.prompt_version : opts.prompt_versions[0] || '';
    document.getElementById('s-recall').value = d.min_recall;
    document.getElementById('s-rep').value = d.max_repeats;
    document.getElementById('s-val').value = d.val_fraction;
    document.getElementById('s-ctx').value = d.context_tokens;
    document.getElementById('s-held').innerHTML = opts.datasets.map(x => `<label class="flex items-center gap-1"><input type="checkbox" value="${UI.esc(x._id)}" ${x.role === 'heldout' ? 'checked' : ''}> ${UI.esc(x.name)} <span class="text-slate-500">(${x.repos})</span></label>`).join('') || '<span class="text-slate-400">No datasets yet.</span>';
    for (const id of ['s-pv', 's-recall']) document.getElementById(id).addEventListener('input', schedulePreview);
    document.getElementById('s-held').addEventListener('change', schedulePreview);
    document.getElementById('s-go').onclick = createSet;

    const base = document.getElementById('t-base');
    base.innerHTML = opts.bases.map(b => `<option value="${UI.esc(b.id)}">${UI.esc(b.label)} (${UI.esc(b.id)})</option>`).join('') + '<option value="">other…</option>';
    base.onchange = applyBase;
    document.getElementById('t-quant').innerHTML = opts.quants.map(q => `<option>${q}</option>`).join('');
    document.getElementById('t-epochs').value = t.epochs; document.getElementById('t-lr').value = t.lr;
    document.getElementById('t-r').value = t.lora_r; document.getElementById('t-alpha').value = t.lora_alpha;
    document.getElementById('t-acc').value = t.grad_accum; document.getElementById('t-max').value = t.max_examples;
    document.getElementById('t-flavor').innerHTML = opts.hf_flavors.map(f => `<option value="${UI.esc(f.id)}">${UI.esc(f.label)}, $${f.price}/h</option>`).join('');
    document.getElementById('t-flavor').value = opts.hf_defaults.flavor;
    document.getElementById('t-timeout').value = opts.hf_defaults.timeout_h;
    document.getElementById('t-compute').onchange = applyBase;
    for (const id of ['t-flavor', 't-timeout']) document.getElementById(id).addEventListener('input', hint);
    applyBase();
    for (const id of ['t-set', 't-len']) document.getElementById(id).addEventListener('input', checkFit);
    document.getElementById('t-set').addEventListener('change', () => { if (num('t-compute') === 'hf') applyBase(); });
    document.getElementById('t-go').onclick = createRun;

    schedulePreview();
    UI.every(4000, load);
    if (params.set) setTimeout(() => showSet(params.set), 300);
  }

  // On a cloud GPU with 48 GB or more the base trains in bf16 (no 4-bit) and
  // the max length follows the set's context instead of a 16 GB budget.
  function applyBase() {
    const id = document.getElementById('t-base').value, b = opts.bases.find(x => x.id === id);
    const hf = num('t-compute') === 'hf', S = sets.find(s => s._id === num('t-set'));
    document.getElementById('t-other-wrap').classList.toggle('hidden', !!id);
    document.querySelectorAll('.t-hf').forEach(e => e.classList.toggle('hidden', !hf));
    document.getElementById('t-note').textContent = b ? b.note : 'Any causal LM with a chat template that supports tools; llama.cpp must be able to convert it.';
    const local = b ? b.max_len : 16384;
    document.getElementById('t-len').value = hf ? Math.max(local, S ? Math.ceil(S.context_tokens * 1.1 / 1024) * 1024 : 32768) : local;
    document.getElementById('t-qlora').checked = hf ? false : b ? b.qlora : true;
    hint(); checkFit();
  }

  function hint() {
    const el = document.getElementById('t-hint');
    if (num('t-compute') !== 'hf') { el.textContent = 'Holds the GPU, so local-model probes wait while it trains.'; return; }
    const f = opts.hf_flavors.find(x => x.id === num('t-flavor')) || {}, h = Number(num('t-timeout')) || 0;
    el.textContent = `Uploads the set to a private dataset repo and trains on a ${f.label}; the merged model comes back through a private model repo for GGUF conversion here. At most $${(f.price * h).toFixed(2)} at the ${h} h limit. Needs HF_TOKEN on the worker and credits on that account.`;
  }

  // Examples longer than max length are dropped, and the longest are the late
  // turns (writing install.md, finishing): the set's context should fit.
  function checkFit() {
    const S = sets.find(s => s._id === num('t-set')), el = document.getElementById('t-fit'), len = Number(num('t-len'));
    const p90 = S && S.manifest ? Math.round(S.manifest.train.p90_chars / 3.5) : 0;
    const over = S && (S.context_tokens > len || p90 > len);
    el.classList.toggle('hidden', !over);
    if (over) el.textContent = `${S._id} was built for a ${Math.round(S.context_tokens / 1024)}K-token context (p90 example ≈ ${Math.round(p90 / 1000)}K tokens), above the ${len}-token max length: long examples, mostly the late turns that write install.md, would be dropped. Build a set with student context ≤ ${len}, or raise max length if the GPU has room.`;
  }

  function schedulePreview() { clearTimeout(previewTimer); previewTimer = setTimeout(preview, 300); }
  async function preview() {
    const gen = ++previewGen;
    const held = [...document.querySelectorAll('#s-held input:checked')].map(i => i.value);
    const q = new URLSearchParams({ min_recall: num('s-recall'), prompt_version: num('s-pv'), heldout: held.join(',') });
    try {
      const p = await UI.get('/api/sft/preview?' + q);
      if (gen !== previewGen) return;
      const el = document.getElementById('s-prov');
      const was = new Map([...el.querySelectorAll('input')].map(i => [i.value, i.checked]));
      el.innerHTML = p.providers.length ? `<table class="simple"><tr><th></th><th>Provider</th><th>Candidate runs</th><th>On held-out repos</th><th>Repos</th><th>Models</th></tr>` +
        p.providers.map(x => `<tr><td><input type="checkbox" value="${UI.esc(x.provider)}" ${was.has(x.provider) ? (was.get(x.provider) ? 'checked' : '') : 'checked'}></td><td>${UI.esc(x.provider)}</td><td>${x.runs}</td><td>${x.heldout_runs}</td><td>${x.repos}</td>
          <td class="text-xs text-slate-400">${x.models.map(m => UI.esc(UI.shortModel(m))).join(', ')}</td></tr>`).join('') + '</table>'
        : '<span class="text-slate-400">No delivered WSL runs match.</span>';
      document.getElementById('s-est').textContent = `${p.candidates} candidate runs before transcript checks; ${p.heldout_repos} held-out repos. Runs that needed nudges, looped or lack a raw transcript are dropped during the build.`;
    } catch (e) { }
  }

  async function createSet() {
    const providers = [...document.querySelectorAll('#s-prov input')];
    const body = { name: num('s-name').trim(), prompt_version: num('s-pv'), min_recall: num('s-recall'), max_repeats: num('s-rep'), val_fraction: num('s-val'),
      context_tokens: num('s-ctx'), include_reasoning: document.getElementById('s-reason').checked,
      heldout_datasets: [...document.querySelectorAll('#s-held input:checked')].map(i => i.value),
      providers: providers.every(i => i.checked) ? [] : providers.filter(i => i.checked).map(i => i.value) };
    if (providers.length && !providers.some(i => i.checked)) return UI.toast('Pick at least one teacher provider', true);
    if (!body.heldout_datasets.length && !confirm('No held-out dataset: every repo can end up in training, so no eval will be clean. Create anyway?')) return;
    const S = await UI.act(() => UI.post('/api/sft/sets', body), 'Queued the build');
    await load(); showSet(S._id);
  }

  async function createRun() {
    const baseSel = num('t-base');
    const body = { sft_set: num('t-set'), base_model: baseSel || num('t-other').trim(), name: num('t-name').trim(), epochs: num('t-epochs'), lr: num('t-lr'),
      lora_r: num('t-r'), lora_alpha: num('t-alpha'), max_len: num('t-len'), grad_accum: num('t-acc'), quant: num('t-quant'), max_examples: num('t-max'),
      priority: num('t-prio'), qlora: document.getElementById('t-qlora').checked, compute: num('t-compute'), hf_flavor: num('t-flavor'), hf_timeout_h: num('t-timeout') };
    if (!body.sft_set) return UI.toast('Build an SFT set first', true);
    await UI.act(() => UI.post('/api/train/runs', body), 'Queued training');
    load();
  }

  async function load() {
    try { [sets, runs] = await Promise.all([UI.get('/api/sft/sets'), UI.get('/api/train/runs')]); } catch { return; }
    if (!document.getElementById('sets')) return;
    const sel = document.getElementById('t-set'), keep = sel.value;
    const ready = sets.filter(s => s.status === 'ready');
    sel.innerHTML = ready.map(s => `<option value="${UI.esc(s._id)}">${UI.esc(s._id)}: ${UI.esc(s.name)} (${s.manifest.train.examples} train examples)</option>`).join('') || '<option value="">no built set yet</option>';
    if (ready.some(s => s._id === keep)) sel.value = keep;
    renderSets(); renderRuns(); checkFit();
  }

  const progressHtml = j => {
    const p = (j && j.progress) || {};
    const frac = p.pct != null ? p.pct / 100 : p.turn && p.max_turns ? p.turn / p.max_turns : null;
    const label = p.pct != null ? `${Math.round(p.pct)}%${p.eta_s != null ? `, about ${UI.dur(p.eta_s)} left` : ''}` : `${p.turn}/${p.max_turns}`;
    return `<div class="text-xs text-slate-300">${UI.esc(p.phase || '')}${p.msg ? ` <span class="text-slate-400">${UI.esc(p.msg)}</span>` : ''}${p.eval_loss ? ` <span class="text-slate-400">val ${p.eval_loss}</span>` : ''}</div>` +
      (frac != null ? `<div class="flex items-center gap-2" title="${UI.esc(p.examples ? p.examples + ' examples' : '')}"><div class="bar flex-1"><div style="width:${Math.round(frac * 100)}%"></div></div><span class="text-xs whitespace-nowrap">${UI.esc(label)}</span></div>` : '');
  };

  function renderSets() {
    const el = document.getElementById('sets');
    if (!sets.length) { el.innerHTML = '<div class="text-slate-400">No SFT sets yet. Create one above; the build runs on the worker that has the raw transcripts.</div>'; return; }
    el.innerHTML = `<table class="simple"><tr><th>Set</th><th>Status</th><th>Kept runs</th><th>Examples (train / val)</th><th>Repos (train / val / held-out)</th><th>Filter</th><th>Context</th><th>Built</th><th></th></tr>` +
      sets.map(s => {
        const m = s.manifest, f = s.filter || {};
        const err = s.status === 'failed' ? (s.error || (s.job_info && s.job_info.error) || '') : '';
        return `<tr><td><button class="text-sky-400 text-left" data-show="${UI.esc(s._id)}">${UI.esc(s._id)}</button> <span class="text-slate-300">${UI.esc(s.name !== s._id ? s.name : '')}</span></td>
          <td>${UI.badge(s.status)}${s.status === 'building' ? progressHtml(s.job_info) : ''}${err ? `<div class="text-xs text-red-300">${UI.esc(err.slice(0, 160))}</div>` : ''}</td>
          <td>${m ? `${m.kept_runs} of ${m.candidates}` : ''}</td>
          <td>${m ? `${m.train.examples} / ${m.val.examples}` : ''}</td>
          <td>${m ? `${m.repos.train} / ${m.repos.val} / ${m.repos.heldout}` : `– / – / ${(s.heldout_repos || []).length}`}</td>
          <td class="text-xs text-slate-400">recall ≥ ${f.min_recall}, ${UI.esc(f.prompt_version)}, repeats &lt; ${f.max_repeats}${(f.providers || []).length ? `, ${f.providers.map(UI.esc).join('+')}` : ', all providers'}</td>
          <td>${Math.round(s.context_tokens / 1024)}K</td>
          <td class="text-xs">${UI.ago(s.built_at)}</td>
          <td class="whitespace-nowrap">${s.status === 'ready' ? `<button class="btn btn-xs" data-train="${UI.esc(s._id)}">Train</button> ` : ''}
            ${['failed', 'cancelled'].includes(s.status) || (s.status === 'ready' && !s.train_runs) ? `<button class="btn btn-xs" data-rebuild="${UI.esc(s._id)}">Rebuild</button> ` : ''}
            ${s.status !== 'building' ? `<button class="btn btn-xs" data-del-set="${UI.esc(s._id)}">Delete</button>` : ''}</td></tr>`;
      }).join('') + '</table>';
    el.querySelectorAll('[data-show]').forEach(b => b.onclick = () => showSet(b.dataset.show));
    el.querySelectorAll('[data-train]').forEach(b => b.onclick = () => { document.getElementById('t-set').value = b.dataset.train; document.getElementById('t-set').scrollIntoView({ behavior: 'smooth', block: 'center' }); });
    el.querySelectorAll('[data-rebuild]').forEach(b => b.onclick = async () => { await UI.act(() => UI.post('/api/sft/set/action', { id: b.dataset.rebuild, action: 'rebuild' }), 'Rebuild queued'); load(); });
    el.querySelectorAll('[data-del-set]').forEach(b => b.onclick = async () => { if (!confirm(`Delete ${b.dataset.delSet}? The files on the worker stay.`)) return; await UI.act(() => UI.post('/api/sft/set/action', { id: b.dataset.delSet, action: 'delete' }), 'Deleted'); load(); });
  }

  const flavorLabel = id => ((opts.hf_flavors || []).find(f => f.id === id) || { label: id }).label;
  const computeHtml = r => {
    const c = r.compute || {}, hf = (r.output || {}).hf;
    if (c.target !== 'hf') return 'local GPU';
    return `Hugging Face ${UI.esc(flavorLabel(c.flavor))}` + (hf && hf.cost_usd != null ? `, $${hf.cost_usd}` : `, ≤ ${c.timeout_h} h`);
  };
  const hfLink = r => {
    const url = ((r.output || {}).hf || {}).url || ((r.job_info || {}).progress || {}).hf_url;
    return url ? `<a class="text-xs text-sky-400" href="${UI.esc(url)}" target="_blank" rel="noopener">Hugging Face job ↗</a>` : '';
  };

  function renderRuns() {
    const el = document.getElementById('runs');
    if (!runs.length) { el.innerHTML = '<div class="text-slate-400">No training runs yet.</div>'; return; }
    el.innerHTML = `<table class="simple"><tr><th>Run</th><th>Base</th><th>Set</th><th>Status</th><th style="width:22%">Progress</th><th>Model</th><th>Loss (train / val)</th><th>Created</th><th></th></tr>` +
      runs.map(r => {
        const o = r.output || {}, mt = o.metrics || {}, p = r.params || {};
        const err = r.status === 'failed' ? (r.error || (r.job_info && r.job_info.error) || '') : '';
        const S = sets.find(s => s._id === r.sft_set), base = opts.bases.find(b => b.id === r.base_model);
        const evalHref = o.gguf ? `#launch?models=${encodeURIComponent([o.gguf, base && base.base_gguf].filter(Boolean).join(','))}${S && S.heldout_datasets && S.heldout_datasets[0] ? `&dataset=${encodeURIComponent(S.heldout_datasets[0])}` : ''}` : '';
        return `<tr><td><button class="text-sky-400 text-left" data-run="${UI.esc(r._id)}">${UI.esc(r._id)}</button> <span class="text-slate-300">${UI.esc(r.name !== r._id ? r.name : '')}</span></td>
          <td title="${UI.esc(r.base_model)}">${UI.esc(r.base_label)}<div class="text-xs text-slate-400">${p.epochs} ep, lr ${p.lr}, r ${p.lora_r}, ${Math.round(p.max_len / 1024)}K${p.qlora ? ', 4-bit' : ''}${p.max_examples ? `, ${p.max_examples} ex.` : ''}</div>
            <div class="text-xs text-slate-400">${computeHtml(r)}</div></td>
          <td>${UI.esc(r.sft_set)}</td>
          <td>${UI.badge(r.status)}${err ? `<div class="text-xs text-red-300">${UI.esc(err.slice(0, 200))}</div>` : ''}</td>
          <td>${['training', 'queued'].includes(r.status) ? progressHtml(r.job_info) + hfLink(r) : ''}</td>
          <td>${o.gguf ? `<span class="code text-xs">${UI.esc(o.gguf)}</span><div class="text-xs text-slate-400">${o.size_gb} GB</div>` : `<span class="text-xs text-slate-500">${UI.esc(r.output_name)}-${UI.esc(r.quant)}.gguf</span>`}</td>
          <td>${mt.train_loss != null ? `${mt.train_loss} / ${mt.eval_loss != null ? mt.eval_loss : '–'}` : ''}</td>
          <td class="text-xs">${UI.ago(r.created_at)}</td>
          <td class="whitespace-nowrap">${evalHref ? `<a class="btn btn-xs btn-primary" href="${evalHref}" title="Probe the held-out repos with the fine-tune and its base model">Evaluate</a> ` : ''}
            ${['queued', 'training'].includes(r.status) ? `<button class="btn btn-xs" data-act="cancel" data-id="${UI.esc(r._id)}">Cancel</button> ` : ''}
            ${['failed', 'cancelled', 'done'].includes(r.status) ? `<button class="btn btn-xs" data-act="retry" data-id="${UI.esc(r._id)}">Retry</button> ` : ''}
            ${r.status !== 'training' ? `<button class="btn btn-xs" data-act="delete" data-id="${UI.esc(r._id)}">Delete</button>` : ''}</td></tr>`;
      }).join('') + '</table>';
    el.querySelectorAll('[data-run]').forEach(b => b.onclick = () => showRun(b.dataset.run));
    el.querySelectorAll('[data-act]').forEach(b => b.onclick = async () => {
      if (b.dataset.act === 'delete' && !confirm(`Delete ${b.dataset.id}? The model file stays in the doctor assets.`)) return;
      await UI.act(() => UI.post('/api/train/run/action', { id: b.dataset.id, action: b.dataset.act }), `${b.dataset.act}: ${b.dataset.id}`); load();
    });
  }

  const kv = obj => Object.entries(obj || {}).map(([k, v]) => `<tr><td>${UI.esc(k)}</td><td>${UI.esc(v)}</td></tr>`).join('');

  async function showSet(id) {
    const s = await UI.get('/api/sft/set?id=' + encodeURIComponent(id));
    const m = s.manifest;
    const body = UI.openDrawer(`SFT set ${s._id}${s.name !== s._id ? `: ${s.name}` : ''}`, '');
    if (!m) {
      body.innerHTML = `<div class="text-sm">${UI.badge(s.status)} ${s.status === 'building' ? progressHtml(s.job_info) : ''}</div>${s.error ? `<div class="log mt-3">${UI.esc(s.error)}</div>` : ''}
        <div class="text-sm text-slate-400 mt-3">Held-out repos: ${(s.heldout_repos || []).map(UI.esc).join(', ') || 'none'}</div>`;
      return;
    }
    const sample = x => `<div class="turn mb-4"><div class="text-xs text-slate-400 mb-1">${UI.esc(UI.shortModel(x.model))} on ${UI.esc(x.repo)}, turn ${x.turn}: ${x.context_messages} messages of context (${Math.round(x.context_chars / 1000)}K chars), last ones and the target:</div>` +
      (x.tail || []).map((t, i, a) => `<div class="mb-1"><span class="badge ${i === a.length - 1 ? 'b-done' : 'b-queued'}">${i === a.length - 1 ? 'target' : UI.esc(t.role)}${t.name ? ' ' + UI.esc(t.name) : ''}</span>
        ${t.content ? `<div class="log mt-1" style="max-height:12rem">${UI.esc(t.content)}</div>` : ''}${(t.calls || []).map(c => `<div class="code text-xs text-sky-300 mt-1 break-all">${UI.esc(c)}</div>`).join('')}</div>`).join('') + '</div>';
    body.innerHTML = `
      <div class="grid grid-cols-2 md:grid-cols-5 gap-3 mb-4">
        <div><div class="kpi">${m.kept_runs}</div><div class="kpi-l">runs kept of ${m.candidates}</div></div>
        <div><div class="kpi">${m.train.examples}</div><div class="kpi-l">train examples</div></div>
        <div><div class="kpi">${m.val.examples}</div><div class="kpi-l">val examples</div></div>
        <div><div class="kpi">${m.repos.train}/${m.repos.val}/${m.repos.heldout}</div><div class="kpi-l">repos train/val/held-out</div></div>
        <div><div class="kpi">${Math.round(m.train.p90_chars / 1000)}K</div><div class="kpi-l">p90 context chars (≈${Math.round(m.train.p90_chars / 3500)}K tokens)</div></div>
      </div>
      <div class="text-xs text-slate-400 mb-3">On ${UI.esc(m.host)}: <span class="code">${UI.esc(m.path)}</span>. Context trimmed as the probe would for ${s.context_tokens} tokens (${m.max_chars} chars). Tools: ${(m.tools.names || []).map(UI.esc).join(', ')} (sha ${UI.esc(m.tools.sha256_16)}).</div>
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-4">
        <div class="lg:col-span-2"><h3 class="lbl">By teacher model</h3><table class="simple"><tr><th>Model</th><th>Kept / candidates</th><th>Train runs</th><th>Val runs</th><th>Held-out runs</th><th>Examples</th><th>Repos</th></tr>
          ${(m.by_model || []).map(x => `<tr><td>${UI.esc(UI.shortModel(x.model))}</td><td>${x.kept} / ${x.candidates}</td><td>${x.train_runs}</td><td>${x.val_runs}</td><td>${x.heldout_runs}</td><td>${x.examples}</td><td>${x.repos}</td></tr>`).join('')}</table></div>
        <div><h3 class="lbl">Runs dropped</h3><table class="simple">${kv(m.dropped_runs) || '<tr><td class="text-slate-400">none</td></tr>'}</table>
          <h3 class="lbl mt-3">Replies kept as context only</h3><table class="simple">${kv(m.skipped_targets) || '<tr><td class="text-slate-400">none</td></tr>'}</table>
          <h3 class="lbl mt-3">Context length (chars)</h3><table class="simple"><tr><th></th><th>p50</th><th>p90</th><th>max</th></tr>
            <tr><td>train</td><td>${m.train.p50_chars}</td><td>${m.train.p90_chars}</td><td>${m.train.max_chars}</td></tr><tr><td>val</td><td>${m.val.p50_chars}</td><td>${m.val.p90_chars}</td><td>${m.val.max_chars}</td></tr></table></div>
      </div>
      <div class="text-sm mb-1"><span class="lbl inline">Val repos:</span> ${(m.val_repos || []).map(UI.esc).join(', ') || 'none'}</div>
      <div class="text-sm mb-4"><span class="lbl inline">Held-out repos:</span> ${(s.heldout_repos || []).map(UI.esc).join(', ') || 'none'}</div>
      <h3 class="lbl">Sample examples</h3>${(s.samples || []).map(sample).join('') || '<div class="text-slate-400 text-sm">none</div>'}`;
  }

  async function showRun(id) {
    const r = runs.find(x => x._id === id); if (!r) return;
    const body = UI.openDrawer(`Training run ${r._id}`, '<div id="tr-head"></div><h3 class="lbl mt-3">Job log</h3><div id="tr-log" class="log">loading…</div>');
    const o = r.output || {};
    body.querySelector('#tr-head').innerHTML = `<table class="simple">
      <tr><td>Base</td><td>${UI.esc(r.base_model)}</td></tr><tr><td>SFT set</td><td>${UI.esc(r.sft_set)}</td></tr>
      <tr><td>Status</td><td>${UI.badge(r.status)} ${r.error ? `<span class="text-red-300">${UI.esc(r.error)}</span>` : ''}</td></tr>
      <tr><td>Compute</td><td>${computeHtml(r)} ${hfLink(r)}</td></tr>
      ${o.hf ? `<tr><td>Hugging Face</td><td>model <a class="text-sky-400" target="_blank" rel="noopener" href="https://huggingface.co/${UI.esc(o.hf.model_repo)}">${UI.esc(o.hf.model_repo)}</a>, data <a class="text-sky-400" target="_blank" rel="noopener" href="https://huggingface.co/datasets/${UI.esc(o.hf.dataset_repo)}">${UI.esc(o.hf.dataset_repo)}</a>${o.hf.running_secs ? `, ran ${UI.dur(o.hf.running_secs)}` : ''}</td></tr>` : ''}
      ${kv(r.params)}<tr><td>quant</td><td>${UI.esc(r.quant)}</td></tr>
      ${o.gguf ? `<tr><td>Model file</td><td class="code">${UI.esc(o.path || o.gguf)}</td></tr><tr><td>Adapter</td><td class="code">${UI.esc(o.adapter || '')}</td></tr><tr><td>Environment</td><td>${UI.esc(o.environment || '')}</td></tr>${kv(o.metrics && { train_loss: o.metrics.train_loss, eval_loss: o.metrics.eval_loss, steps: o.metrics.steps, seconds: o.metrics.seconds })}` : ''}
      </table>`;
    const logEl = body.querySelector('#tr-log');
    const pull = async () => { try { const j = await UI.get('/api/job?id=' + encodeURIComponent(r.job)); logEl.textContent = (j.log || []).join('\n') || '(no output yet)'; } catch (e) { logEl.textContent = e.message; } };
    await pull();
    const t = setInterval(pull, 4000);
    UI.onDrawerClose = () => clearInterval(t);
  }

  return { mount };
})());
