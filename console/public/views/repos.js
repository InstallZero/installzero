// Repos: the catalog, adding repos from GitHub, the recipe and facts editors,
// agent drafts, snapshot builds, and adding a selection to a dataset.
UI.register('repos', (() => {
  let table, models = [];

  async function mount(root, params) {
    root.innerHTML = `
      <div class="card mb-4 flex flex-wrap items-end gap-3">
        <div class="flex-1 min-w-[320px]"><label class="lbl">Add a repo (GitHub URL or owner/name)</label>
          <div class="flex gap-2"><input id="add-url" class="inp flex-1" placeholder="https://github.com/owner/name"><input id="add-cat" class="inp w-40" placeholder="category"><button id="add" class="btn btn-primary">Add</button><label class="text-xs flex items-center gap-1"><input id="add-draft" type="checkbox" checked> draft recipe</label></div></div>
        <div><label class="lbl">Selection</label>
          <div class="flex gap-2"><select id="ds" class="inp"></select><button id="to-ds" class="btn">Add selected to dataset</button><button id="build-sel" class="btn">Build selected</button><button id="draft-sel" class="btn">Draft selected</button></div></div>
      </div>
      <div class="card">
        <div class="flex items-center gap-2 mb-3"><h2 class="!mb-0 mr-auto">Repos</h2>
          <select id="f-stage" class="inp"><option value="">all stages</option><option>ready</option><option>recipe</option><option>draft</option><option>drafting</option><option>building</option><option>build-failed</option><option>unhealthy</option><option>metadata</option></select>
          <select id="f-status" class="inp"><option value="">all statuses</option><option>selected</option><option>candidate</option><option>dropped</option></select>
          <span id="sel-count" class="text-xs text-slate-400"></span></div>
        <div id="repos"></div>
      </div>`;

    table = new Tabulator('#repos', {
      layout: 'fitColumns', height: '68vh', index: 'id', selectableRows: true, placeholder: 'No repos.',
      columns: [
        { formatter: 'rowSelection', titleFormatter: 'rowSelection', width: 36, hozAlign: 'center', headerSort: false },
        { title: 'Repo', field: 'id', minWidth: 220, headerFilter: 'input' },
        { title: 'Bench id', field: 'bench_id', width: 110, headerFilter: 'input' },
        { title: 'Category', field: 'category', width: 120, headerFilter: 'input' },
        { title: 'Status', field: 'status', width: 90 },
        { title: 'Stage', field: 'stage', width: 105, formatter: c => UI.badge(c.getValue()) },
        { title: 'Diff.', field: 'difficulty', width: 70, hozAlign: 'right', formatter: c => c.getValue() != null ? Number(c.getValue()).toFixed(1) : '' },
        { title: 'Stars', field: 'stars', width: 80, hozAlign: 'right', formatter: c => c.getValue() != null ? (c.getValue() >= 1000 ? (c.getValue() / 1000).toFixed(1) + 'k' : c.getValue()) : '' },
        { title: 'Facts', field: 'facts', width: 65, hozAlign: 'right' },
        { title: 'Snapshot', field: 'snapshot', width: 130, formatter: c => { const s = c.getValue(); return s && s.status === 'ready' ? `${s.size_gb ? s.size_gb + ' GB' : 'ready'}` : ''; }, sorter: (a, b) => ((a && a.size_gb) || 0) - ((b && b.size_gb) || 0) },
        { title: 'Ports', field: 'snapshot', width: 130, formatter: c => { const s = c.getValue(); return s && s.ports ? UI.esc(s.ports.join(' ')) : ''; }, headerSort: false },
        { title: 'OS', field: 'os', width: 60 },
      ],
    });
    table.on('rowClick', (e, row) => { if (!e.target.closest('.tabulator-cell[tabulator-field=""]') && !e.target.closest('input')) openRepo(row.getData().id); });
    table.on('rowSelectionChanged', d => { root.querySelector('#sel-count').textContent = d.length ? `${d.length} selected` : ''; });
    root.querySelector('#f-stage').onchange = applyFilters;
    root.querySelector('#f-status').onchange = applyFilters;

    root.querySelector('#add').onclick = async () => {
      const url = root.querySelector('#add-url').value.trim(); if (!url) return;
      const r = await UI.act(() => UI.post('/api/repos', { url, category: root.querySelector('#add-cat').value.trim() || undefined }), 'Added');
      if (root.querySelector('#add-draft').checked) await UI.act(() => UI.post('/api/jobs', { type: 'draft', repo: r._id, model: models[0] }), 'Draft queued');
      root.querySelector('#add-url').value = '';
      await load(); openRepo(r._id);
    };
    root.querySelector('#to-ds').onclick = addSelectionToDataset;
    root.querySelector('#build-sel').onclick = () => bulkJobs('build');
    root.querySelector('#draft-sel').onclick = () => bulkJobs('draft');

    models = await UI.modelOptions();
    await loadDatasets(root);
    await load();
    if (params.id) openRepo(params.id);
    UI.every(10000, load);
  }

  function applyFilters() {
    const stage = document.getElementById('f-stage').value, status = document.getElementById('f-status').value;
    table.setFilter(d => (!stage || d.stage === stage) && (!status || d.status === status));
  }
  async function load() { try { await table.replaceData(await UI.get('/api/repos')); applyFilters(); } catch (e) { } }
  async function loadDatasets(root) {
    const ds = await UI.get('/api/datasets');
    root.querySelector('#ds').innerHTML = '<option value="__new">+ new dataset…</option>' + ds.filter(d => !d.frozen).map(d => `<option value="${UI.esc(d._id)}">${UI.esc(d.name)} (${d.repos.length})</option>`).join('');
  }

  async function addSelectionToDataset() {
    const ids = table.getSelectedData().map(d => d.id);
    if (!ids.length) return UI.toast('Select repos first (checkboxes)', true);
    let ds = document.getElementById('ds').value;
    if (ds === '__new') {
      const name = prompt('Name of the new dataset'); if (!name) return;
      const d = await UI.act(() => UI.post('/api/datasets', { name, repos: ids }), `Created ${name} with ${ids.length} repos`);
      location.hash = `#datasets?id=${encodeURIComponent(d._id)}`; return;
    }
    await UI.act(() => UI.put('/api/dataset?id=' + encodeURIComponent(ds), { add: ids }), `Added ${ids.length} repos`);
    loadDatasets(document.getElementById('view'));
  }
  async function bulkJobs(type) {
    const ids = table.getSelectedData().map(d => d.id);
    if (!ids.length) return UI.toast('Select repos first (checkboxes)', true);
    let ok = 0;
    for (const id of ids) { try { await UI.post('/api/jobs', { type, repo: id, model: models[0] }); ok++; } catch (e) { UI.toast(`${id}: ${e.message}`, true); } }
    UI.toast(`Queued ${ok} ${type} jobs`); load();
  }

  // ---- repo drawer ----------------------------------------------------------------

  async function openRepo(id) {
    const r = await UI.get('/api/repo?id=' + encodeURIComponent(id));
    const body = UI.openDrawer(id, `
      <div class="flex flex-wrap items-center gap-2 mb-3">${UI.badge(r.stage)}<span class="text-slate-400 text-sm">bench id <b class="text-slate-200">${UI.esc(r.bench_id || '-')}</b></span>
        <a class="text-sky-400 text-sm" href="${UI.esc(r.url)}" target="_blank">${UI.esc(r.url)}</a>
        <span class="ml-auto flex gap-2">
          <select id="r-model" class="inp">${models.map(m => `<option>${UI.esc(m)}</option>`).join('')}</select>
          <button class="btn" data-job="draft">Draft recipe</button><button class="btn" data-job="build">Build snapshot</button><button class="btn btn-primary" data-job="probe">Probe once</button>
        </span></div>
      <div class="tabs">${['overview', 'recipe', 'facts', 'draft', 'snapshot', 'runs'].map(t => `<button data-tab="${t}">${t[0].toUpperCase() + t.slice(1)}</button>`).join('')}</div>
      <div id="tab"></div>`);
    body.querySelectorAll('[data-job]').forEach(b => b.onclick = async () => {
      const type = b.dataset.job, model = body.querySelector('#r-model').value;
      await UI.act(() => UI.post('/api/jobs', { type, repo: id, model }), `${type} queued`);
      openRepo(id);
    });
    const show = t => {
      body.querySelectorAll('[data-tab]').forEach(b => b.classList.toggle('active', b.dataset.tab === t));
      const el = body.querySelector('#tab');
      ({ overview, recipe, facts, draft, snapshot, runs })[t](el, r);
    };
    body.querySelectorAll('[data-tab]').forEach(b => b.onclick = () => show(b.dataset.tab));
    show(r.draft && r.draft.status === 'ready' && !r.recipe ? 'draft' : 'overview');
  }

  function overview(el, r) {
    const g = r.github || {}, rb = r.rubric || {};
    el.innerHTML = `<div class="grid grid-cols-2 gap-4 text-sm">
      <div class="card"><h2>GitHub</h2><div>${UI.esc(g.description || '')}</div>
        <div class="text-slate-400 mt-2">${g.stars != null ? `★ ${g.stars}` : ''} ${UI.esc(g.language || '')} ${UI.esc(g.license || '')} ${g.latest_release ? `· release ${UI.esc(g.latest_release)}` : ''} ${g.pushed_at ? `· pushed ${UI.ago(g.pushed_at)}` : ''}</div></div>
      <div class="card"><h2>Rubric</h2>${rb.difficulty != null ? `<div class="kpi">${Number(rb.difficulty).toFixed(1)}</div><div class="text-slate-400 text-xs">difficulty (1-10) ${rb.source ? `· ${UI.esc(rb.source)}` : ''}</div>
        <div class="text-xs mt-2">${Object.entries(rb.factors || {}).map(([k, v]) => `${UI.esc(k.replace(/_/g, ' '))} ${v}`).join(' · ')}</div>
        ${rb.why ? `<div class="text-xs text-slate-400 mt-1">${UI.esc(rb.why)}</div>` : ''}` : '<span class="text-slate-400">not scored</span>'}</div>
      <div class="card col-span-2"><h2>Catalog</h2>
        <div class="grid grid-cols-4 gap-3">
          <div><label class="lbl">Category</label><input id="o-cat" class="inp w-full" value="${UI.esc(r.category || '')}"></div>
          <div><label class="lbl">Status</label><select id="o-status" class="inp w-full">${['candidate', 'selected', 'dropped'].map(s => `<option ${r.status === s ? 'selected' : ''}>${s}</option>`).join('')}</select></div>
          <div><label class="lbl">Bench id (snapshot name)</label><input id="o-bid" class="inp w-full" value="${UI.esc(r.bench_id || '')}"></div>
          <div><label class="lbl">OS / variant</label><input class="inp w-full" disabled value="${UI.esc([r.os, r.variant].filter(Boolean).join(' / '))}"></div>
          <div class="col-span-4"><label class="lbl">Notes (shown to scorers, not to the probe)</label><textarea id="o-notes" class="inp w-full" rows="2">${UI.esc(r.install_notes || r.why || '')}</textarea></div>
        </div><button id="o-save" class="btn btn-primary mt-3">Save</button><button id="o-del" class="btn btn-danger mt-3 ml-2">Delete repo</button></div></div>`;
    el.querySelector('#o-save').onclick = () => UI.act(() => UI.put('/api/repo?id=' + encodeURIComponent(r._id), { category: el.querySelector('#o-cat').value, status: el.querySelector('#o-status').value, bench_id: el.querySelector('#o-bid').value.trim() || undefined, install_notes: el.querySelector('#o-notes').value }), 'Saved').then(load);
    el.querySelector('#o-del').onclick = async () => { if (confirm(`Delete ${r._id} from the catalog? Results stay.`)) { await UI.act(() => UI.del('/api/repo?id=' + encodeURIComponent(r._id)), 'Deleted'); UI.closeDrawer(); load(); } };
  }

  function recipeForm(x, prefix) {
    x = x || {};
    return `<div class="grid grid-cols-4 gap-3 text-sm">
      <div class="col-span-2"><label class="lbl">Install target (the probe is pointed here)</label><input id="${prefix}-target" class="inp w-full code" value="${UI.esc(x.target || '')}"></div>
      <div><label class="lbl">Ready timeout (s)</label><input id="${prefix}-timeout" type="number" class="inp w-full" value="${x.ready_timeout_sec || 300}"></div>
      <div><label class="lbl">Include snippets</label><input id="${prefix}-include" class="inp w-full code" value="${UI.esc((x.include || []).join(' '))}" placeholder="_docker.sh"></div>
      <div class="col-span-4"><label class="lbl">Ready check (bash, exit 0 when the install is up)</label><input id="${prefix}-ready" class="inp w-full code" value="${UI.esc(x.ready || '')}"></div>
      <div class="col-span-4"><label class="lbl">Recipe (bash, runs as root in a fresh Ubuntu 24.04 with systemd)</label><textarea id="${prefix}-script" class="inp w-full code" rows="22" spellcheck="false">${UI.esc(x.script || '')}</textarea></div>
      <div class="col-span-2"><label class="lbl">Remove before snapshot (installer leftovers, one per line)</label><textarea id="${prefix}-remove" class="inp w-full code" rows="3">${UI.esc((x.remove || []).join('\n'))}</textarea></div>
      <div class="col-span-2"><label class="lbl">Expected listening ports (scheduling until measured)</label><input id="${prefix}-ports" class="inp w-full code" value="${UI.esc((x.ports_expected || []).join(' '))}"></div>
    </div>`;
  }
  function readRecipe(el, prefix) {
    const v = s => el.querySelector(`#${prefix}-${s}`).value;
    return { target: v('target').trim(), ready_timeout_sec: Number(v('timeout')), include: v('include').split(/[\s,]+/).filter(Boolean), ready: v('ready'), script: v('script'),
      remove: v('remove').split('\n').map(s => s.trim()).filter(Boolean), ports_expected: v('ports').split(/[\s,]+/).map(Number).filter(Boolean) };
  }
  function recipe(el, r) {
    el.innerHTML = recipeForm(r.recipe, 'rc') + `<div class="flex gap-2 mt-3"><button id="rc-save" class="btn btn-primary">Save recipe</button><span class="text-xs text-slate-400 self-center">${r.snapshot && r.snapshot.stale ? 'The snapshot was built from an older recipe; rebuild it.' : ''}</span></div>`;
    el.querySelector('#rc-save').onclick = async () => { await UI.act(() => UI.put('/api/repo?id=' + encodeURIComponent(r._id), { recipe: readRecipe(el, 'rc') }), 'Recipe saved'); load(); };
  }

  function factsEditor(el, list, onSave, saveLabel) {
    const t = new Tabulator(el.querySelector('.facts-table'), {
      layout: 'fitColumns', data: JSON.parse(JSON.stringify(list || [])), reactiveData: false, height: '52vh',
      columns: [
        { title: 'id', field: 'id', width: 170, editor: 'input' },
        { title: 'category', field: 'category', width: 120, editor: 'list', editorParams: { values: ['config', 'components', 'host', 'operations', 'version', 'network', 'data', 'security'], autocomplete: true, freetext: true } },
        { title: 'weight', field: 'weight', width: 70, editor: 'number', hozAlign: 'right' },
        { title: 'regex (case-insensitive, multiline) the install doc must match', field: 're', editor: 'input', formatter: c => `<span class="code">${UI.esc(c.getValue())}</span>` },
        { title: '', width: 40, headerSort: false, formatter: () => '✕', cellClick: (e, c) => c.getRow().delete() },
      ],
    });
    el.querySelector('.f-add').onclick = () => t.addRow({ id: 'new_fact', category: 'config', weight: 1, re: '' }, true);
    el.querySelector('.f-save').textContent = saveLabel || 'Save facts';
    el.querySelector('.f-save').onclick = () => onSave(t.getData());
    el.querySelector('.f-test').onclick = () => {
      const text = el.querySelector('.f-sample').value, data = t.getData();
      const hits = data.filter(f => { try { return new RegExp(f.re, 'im').test(text); } catch { return false; } });
      const w = data.reduce((a, f) => a + Number(f.weight || 0), 0), hw = hits.reduce((a, f) => a + Number(f.weight || 0), 0);
      el.querySelector('.f-result').innerHTML = `${hits.length}/${data.length} facts, weighted recall <b>${w ? Math.round(hw / w * 100) : 0}%</b>. Missed: ${data.filter(f => !hits.includes(f)).map(f => `<span class="code text-red-300">${UI.esc(f.id)}</span>`).join(', ') || 'none'}`;
    };
    return t;
  }
  const factsHtml = `<div class="facts-table"></div>
    <div class="flex gap-2 mt-2"><button class="btn f-add">Add fact</button><button class="btn btn-primary f-save">Save facts</button></div>
    <details class="mt-3"><summary class="text-sm text-slate-400 cursor-pointer">Test the facts against an install document</summary>
      <textarea class="inp w-full code f-sample mt-2" rows="8" placeholder="paste an install.md here"></textarea><button class="btn f-test mt-2">Test</button> <span class="f-result text-sm ml-2"></span></details>`;
  function facts(el, r) {
    el.innerHTML = factsHtml;
    factsEditor(el, r.facts, async data => { await UI.act(() => UI.put('/api/repo?id=' + encodeURIComponent(r._id), { facts: data }), 'Facts saved'); load(); });
  }

  function draft(el, r) {
    const d = r.draft;
    if (!d) { el.innerHTML = `<div class="text-slate-400 text-sm">No draft. "Draft recipe" asks the model to read the repo's docs and write a recipe and facts for you to review.</div>`; return; }
    if (d.status === 'drafting') { el.innerHTML = `<div class="text-sm">${UI.badge('drafting')} job <a class="text-sky-400" href="javascript:QueueView.openJob('${d.job}')">${UI.esc(d.job)}</a> is running.</div>`; return; }
    if (d.status === 'failed') { el.innerHTML = `<div class="text-sm">${UI.badge('failed')} <span class="text-red-300">${UI.esc(d.error)}</span></div>`; return; }
    el.innerHTML = `<div class="text-sm mb-3 flex flex-wrap gap-3 items-center"><span>Drafted by <b>${UI.esc(d.model)}</b> ${UI.ago(d.drafted_at)}</span>${d.accepted_at ? `<span class="text-emerald-300">accepted ${UI.ago(d.accepted_at)}</span>` : ''}
        <button id="d-accept" class="btn btn-primary">Accept as recipe and facts</button></div>
      ${d.notes ? `<div class="card text-sm mb-3"><h2>Agent notes</h2><div class="whitespace-pre-wrap">${UI.esc(d.notes)}</div></div>` : ''}
      <div class="card mb-3"><h2>Recipe</h2>${recipeForm(d.recipe, 'dr')}</div>
      <div class="card mb-3"><h2>Facts (${(d.facts || []).length})</h2>${factsHtml}</div>
      <div class="card"><h2>Redraft</h2><textarea id="d-fb" class="inp w-full" rows="3" placeholder="what to change, e.g. use the official compose file; pin 2.11; facts should cover the reverse proxy"></textarea>
        <button id="d-redo" class="btn mt-2">Redraft with this feedback</button></div>`;
    let editedFacts = d.facts;
    const ft = factsEditor(el.querySelectorAll('.card')[d.notes ? 2 : 1], d.facts, data => { editedFacts = data; UI.toast('Kept your fact edits; Accept saves them'); }, 'Keep edits');
    el.querySelector('#d-accept').onclick = async () => {
      await UI.act(() => UI.put('/api/repo?id=' + encodeURIComponent(r._id), { recipe: readRecipe(el, 'dr'), facts: ft.getData(), accept_draft: true }), 'Accepted: recipe and facts saved');
      load(); openRepo(r._id);
    };
    el.querySelector('#d-redo').onclick = async () => {
      await UI.act(() => UI.post('/api/jobs', { type: 'draft', repo: r._id, model: document.getElementById('r-model').value, feedback: el.querySelector('#d-fb').value }), 'Redraft queued');
      openRepo(r._id);
    };
  }

  function snapshot(el, r) {
    const s = r.snapshot || {};
    el.innerHTML = `<div class="grid grid-cols-4 gap-3 text-sm mb-3">
      <div class="card"><div class="kpi-l">Status</div><div class="mt-1">${s.status ? UI.badge(s.status === 'ready' ? 'ready' : s.status === 'failed' ? 'failed' : s.status) : '<span class="text-slate-400">none</span>'} ${s.stale ? '<span class="text-amber-300 text-xs">stale</span>' : ''}</div></div>
      <div class="card"><div class="kpi-l">Size</div><div class="kpi">${s.size_gb ? s.size_gb + ' GB' : '-'}</div></div>
      <div class="card"><div class="kpi-l">Built</div><div class="mt-1">${s.built_at ? new Date(s.built_at).toLocaleString() : '-'}</div></div>
      <div class="card"><div class="kpi-l">Listening ports</div><div class="mt-1 code">${s.ports && s.ports.length ? s.ports.join(' ') : 'unknown (runs alone)'}</div></div></div>
      ${s.error ? `<div class="text-red-300 text-sm mb-2">${UI.esc(s.error)}</div>` : ''}
      ${s.job ? `<button class="btn mb-2" onclick="QueueView.openJob('${s.job}')">Open build job log</button>` : ''}
      ${s.log_tail ? `<div class="log">${UI.esc(s.log_tail)}</div>` : ''}
      ${s.status === 'failed' ? `<button id="s-fix" class="btn btn-primary mt-3">Ask the agent to fix the recipe from this log</button>` : ''}`;
    const fix = el.querySelector('#s-fix');
    if (fix) fix.onclick = async () => { await UI.act(() => UI.post('/api/jobs', { type: 'draft', repo: r._id, model: document.getElementById('r-model').value, feedback: 'The build failed; fix the recipe using the build log.' }), 'Fix draft queued'); openRepo(r._id); };
  }

  async function runs(el, r) {
    el.innerHTML = '<div class="text-slate-400 text-sm">loading…</div>';
    const rows = (await UI.get('/api/results?limit=2000')).filter(x => x.repo === r.bench_id || x.bench_repo === r._id);
    el.innerHTML = rows.length ? `<table class="simple"><tr><th>When</th><th>Model</th><th>Recall</th><th>Facts</th><th>Checks</th><th>Turns</th><th>Time</th><th>Missed</th></tr>` +
      rows.map(x => { const st = (x.probe && x.probe.stats) || {}, lc = x.live_checks; return `<tr><td>${UI.ago(x.run_at)}</td><td>${UI.esc(UI.shortModel(x.model))}</td>
        <td>${x.delivered ? `<b>${UI.pct(x.recall)}</b>` : '<span class="text-amber-300">not delivered</span>'}</td><td>${x.delivered ? `${x.facts_hit}/${x.facts}` : ''}</td>
        <td>${lc ? `${lc.passed}/${(lc.passed || 0) + (lc.failed || 0) + (lc.untested || 0)}` : ''}</td><td>${st.model_turns || ''}</td><td>${UI.dur(st.seconds)}</td>
        <td class="text-xs text-red-300">${(x.misses || []).map(m => UI.esc(m.id)).join(', ')}</td></tr>`; }).join('') + '</table>' : '<div class="text-slate-400 text-sm">No runs yet.</div>';
  }

  return { mount, unmount() { if (table) table.destroy(); table = null; } };
})());
