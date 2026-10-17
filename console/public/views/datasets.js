// Datasets: named sets of repos to evaluate together. Freeze one to keep
// comparisons across models and prompt versions on the same repos.
UI.register('datasets', (() => {
  let all = [], repoList = [], selected = null;

  async function mount(root, params) {
    root.innerHTML = `<div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <div class="flex flex-col gap-4">
        <div class="card"><h2>New dataset</h2>
          <input id="n-name" class="inp w-full mb-2" placeholder="name, e.g. WSL pilot 10">
          <input id="n-desc" class="inp w-full mb-2" placeholder="description">
          <div class="grid grid-cols-3 gap-2 mb-2">
            <select id="n-from" class="inp col-span-3"><option value="empty">empty (add repos from the Repos tab)</option><option value="runnable">all repos with a ready snapshot</option><option value="filter">filter: category and difficulty</option></select>
            <input id="n-cat" class="inp col-span-2" placeholder="category contains"><input id="n-diff" type="number" step="0.5" class="inp" placeholder="min diff.">
          </div>
          <button id="n-go" class="btn btn-primary">Create</button></div>
        <div class="card"><h2>Datasets</h2><div id="list" class="flex flex-col gap-1"></div></div>
      </div>
      <div class="card lg:col-span-2" id="detail"><div class="text-slate-400 text-sm">Pick a dataset.</div></div></div>`;
    root.querySelector('#n-go').onclick = create;
    await load();
    if (params.id) show(params.id); else if (all[0]) show(all[0]._id);
  }

  const roleBadge = r => r === 'tuning' ? '<span class="badge b-recipe">tuning</span>' : r === 'heldout' ? '<span class="badge b-done">held-out</span>' : '';

  async function load() {
    [all, repoList] = await Promise.all([UI.get('/api/datasets'), UI.get('/api/repos')]);
    document.getElementById('list').innerHTML = all.map(d => `<button class="text-left px-2 py-1.5 rounded hover:bg-slate-800 ${selected === d._id ? 'bg-slate-800' : ''}" data-id="${UI.esc(d._id)}">
      <div class="text-sm">${UI.esc(d.name)} ${d.frozen ? '<span class="badge b-cancelled">frozen</span>' : ''} ${roleBadge(d.role)}</div>
      <div class="text-xs text-slate-400">${d.repos.length} repos · ${d.runnable} runnable</div></button>`).join('') || '<div class="text-slate-400 text-sm">None yet.</div>';
    document.querySelectorAll('#list [data-id]').forEach(b => b.onclick = () => show(b.dataset.id));
  }

  async function create() {
    const name = document.getElementById('n-name').value.trim(); if (!name) return UI.toast('Name it first', true);
    const from = document.getElementById('n-from').value;
    let ids = [];
    if (from === 'runnable') ids = repoList.filter(r => r.runnable).map(r => r.id);
    if (from === 'filter') {
      const cat = document.getElementById('n-cat').value.trim().toLowerCase(), min = Number(document.getElementById('n-diff').value || 0);
      ids = repoList.filter(r => (!cat || String(r.category || '').toLowerCase().includes(cat)) && (r.difficulty || 0) >= min && r.status !== 'dropped').map(r => r.id);
    }
    const d = await UI.act(() => UI.post('/api/datasets', { name, description: document.getElementById('n-desc').value.trim(), repos: ids }), `Created with ${ids.length} repos`);
    await load(); show(d._id);
  }

  function show(id) {
    selected = id;
    const d = all.find(x => x._id === id); if (!d) return;
    document.querySelectorAll('#list [data-id]').forEach(b => b.classList.toggle('bg-slate-800', b.dataset.id === id));
    const byId = new Map(repoList.map(r => [r.id, r]));
    const rows = d.repos.map(rid => byId.get(rid) || { id: rid, stage: 'missing' });
    const el = document.getElementById('detail');
    el.innerHTML = `<div class="flex flex-wrap items-start gap-3 mb-3">
        <div class="flex-1 min-w-[260px]"><input id="d-name" class="inp w-full text-base mb-1" value="${UI.esc(d.name)}" ${d.frozen ? 'disabled' : ''}>
          <input id="d-desc" class="inp w-full" value="${UI.esc(d.description || '')}" placeholder="description" ${d.frozen ? 'disabled' : ''}></div>
        <div class="flex gap-2 flex-wrap">
          <a class="btn btn-primary" href="#launch?dataset=${encodeURIComponent(d._id)}">Run eval</a>
          <a class="btn" href="#analysis?dataset=${encodeURIComponent(d._id)}">Analyze</a>
          ${d.frozen ? '' : '<button id="d-save" class="btn">Save</button>'}
          <select id="d-role" class="inp" title="Tuning datasets feed lesson drafting; held-out datasets never do, so they measure whether lessons generalize">
            <option value="">role: none</option><option value="tuning">role: tuning (feeds lessons)</option><option value="heldout">role: held-out (measures lessons)</option></select>
          <button id="d-freeze" class="btn">${d.frozen ? 'Unfreeze' : 'Freeze'}</button>
          <button id="d-build" class="btn">Build missing snapshots</button>
          <button id="d-del" class="btn btn-danger">Delete</button></div></div>
      <div class="text-sm text-slate-400 mb-2">${d.repos.length} repos, ${d.runnable} runnable now. ${d.frozen ? 'Frozen: membership is locked so results stay comparable.' : 'Add repos from the Repos tab (select, then "Add selected to dataset").'}</div>
      <table class="simple"><tr><th>Repo</th><th>Bench id</th><th>Category</th><th>Diff.</th><th>Stage</th><th>Snapshot</th><th>Facts</th><th></th></tr>
        ${rows.map(r => `<tr><td><a class="text-sky-400" href="#repos?id=${encodeURIComponent(r.id)}">${UI.esc(r.id)}</a></td><td>${UI.esc(r.bench_id || '')}</td><td>${UI.esc(r.category || '')}</td>
          <td>${r.difficulty != null ? Number(r.difficulty).toFixed(1) : ''}</td><td>${UI.badge(r.stage)}</td><td>${r.snapshot && r.snapshot.status === 'ready' ? (r.snapshot.size_gb || '?') + ' GB' : ''}</td><td>${r.facts || 0}</td>
          <td>${d.frozen ? '' : `<button class="btn btn-xs" data-rm="${UI.esc(r.id)}">Remove</button>`}</td></tr>`).join('')}</table>`;
    const put = body => UI.put('/api/dataset?id=' + encodeURIComponent(d._id), body);
    const save = el.querySelector('#d-save');
    if (save) save.onclick = async () => { await UI.act(() => put({ name: el.querySelector('#d-name').value, description: el.querySelector('#d-desc').value }), 'Saved'); await load(); show(id); };
    const role = el.querySelector('#d-role');
    role.value = d.role || '';
    role.onchange = async () => { await UI.act(() => put({ role: role.value }), 'Role saved'); await load(); show(id); };
    el.querySelector('#d-freeze').onclick = async () => { await UI.act(() => put({ frozen: !d.frozen }), d.frozen ? 'Unfrozen' : 'Frozen'); await load(); show(id); };
    el.querySelector('#d-del').onclick = async () => { if (!confirm(`Delete dataset ${d.name}? Results stay.`)) return; await UI.act(() => UI.del('/api/dataset?id=' + encodeURIComponent(d._id)), 'Deleted'); selected = null; await load(); document.getElementById('detail').innerHTML = ''; };
    el.querySelector('#d-build').onclick = async () => {
      const todo = rows.filter(r => ['recipe', 'build-failed', 'unhealthy'].includes(r.stage));
      let n = 0; for (const r of todo) { try { await UI.post('/api/jobs', { type: 'build', repo: r.id }); n++; } catch (e) { UI.toast(`${r.id}: ${e.message}`, true); } }
      UI.toast(`Queued ${n} builds${rows.some(r => ['metadata', 'draft', 'drafting'].includes(r.stage)) ? '; repos without an accepted recipe were skipped' : ''}`);
    };
    el.querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => { await UI.act(() => put({ remove: [b.dataset.rm] })); await load(); show(id); });
  }

  return { mount };
})());
