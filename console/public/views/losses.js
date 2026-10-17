// Losses: why the probe misses facts, and the loop that turns recurring
// misses into probe guidance. A diagnosis (one hosted-model pass per run over
// its transcript, document and recipe) gives each miss a cause: probe losses
// (never looked, out of budget, found but not written) feed lesson sets;
// benchmark bugs (regex too strict, not observable) are fixed in the facts.
UI.register('losses', (() => {
  let rows = [], misses = [], table, sets = [], dsList = [], benchToRepo = new Map(), hosted = [];
  const PLOT = { paper_bgcolor: 'rgba(0,0,0,0)', plot_bgcolor: 'rgba(0,0,0,0)', font: { color: '#cbd5e1', size: 11 }, margin: { l: 50, r: 10, t: 10, b: 60 } };
  const CFG = { displayModeBar: false, responsive: true };
  const COLORS = { not_explored: '#ef4444', out_of_budget: '#eab308', found_not_written: '#0ea5e9', written_unmatched: '#a855f7', not_observable: '#c084fc', unclear: '#64748b', undiagnosed: '#334155', no_transcript: '#1e293b' };
  const CAUSES = Object.keys(COLORS);
  const label = c => (ResultView.CAUSE[c] || [c])[0];

  async function mount(root, params) {
    root.innerHTML = `
      <div class="card mb-4 flex flex-wrap gap-3 items-end">
        <div><label class="lbl">Dataset</label><select id="l-ds" class="inp"><option value="">all repos</option></select></div>
        <div><label class="lbl">Campaign</label><select id="l-cmp" class="inp"><option value="">any</option></select></div>
        <div><label class="lbl">Model</label><select id="l-model" class="inp"><option value="">any</option></select></div>
        <div><label class="lbl">Prompt version</label><select id="l-prompt" class="inp"><option value="">any</option></select></div>
        <label class="flex items-center gap-1 text-sm pb-1"><input id="l-tuned" type="checkbox" checked> exclude repos the guidance was tuned on</label>
      </div>
      <div id="l-kpis" class="grid grid-cols-2 md:grid-cols-6 gap-3 mb-4"></div>
      <div class="card mb-4 flex flex-wrap items-center gap-3 text-sm" id="l-actions"></div>
      <div class="grid grid-cols-1 xl:grid-cols-2 gap-4">
        <div class="card"><h2>Weight missed per run, by category and cause</h2><div id="l-cat" style="height:320px"></div></div>
        <div class="card"><h2>Cause mix by model and prompt version</h2><div id="l-model-plot" style="height:320px"></div></div>
        <div class="card xl:col-span-2"><div class="flex items-center mb-2"><h2 class="flex-1" style="margin:0">Lesson sets</h2><button id="l-draft" class="btn btn-primary">Draft lessons</button></div>
          <div class="text-xs text-slate-400 mb-2">Drafted from diagnosed probe losses on tuning datasets, reviewed here, then activated and picked as probe guidance in New eval. Measure an active set on a held-out dataset; the analysis compares prompt versions.</div>
          <div id="l-sets"></div></div>
        <div class="card xl:col-span-2"><h2>Benchmark issues (fix the facts, not the probe)</h2><div id="l-bench" class="overflow-auto" style="max-height:360px"></div></div>
        <div class="card xl:col-span-2"><h2>Misses</h2><div id="l-table"></div></div>
      </div>`;
    const [ds, cs, ls, rl, m] = await Promise.all([UI.get('/api/datasets'), UI.get('/api/campaigns'), UI.get('/api/lessons'), UI.get('/api/repos'), UI.get('/api/models')]);
    dsList = ds; sets = ls; hosted = m.hosted; benchToRepo = new Map(rl.map(r => [r.bench_id, r.id]));
    root.querySelector('#l-ds').insertAdjacentHTML('beforeend', ds.map(d => `<option value="${UI.esc(d._id)}">${UI.esc(d.name)}${d.role ? ` [${d.role === 'heldout' ? 'held-out' : 'tuning'}]` : ''}</option>`).join(''));
    root.querySelector('#l-cmp').insertAdjacentHTML('beforeend', cs.map(c => `<option value="${UI.esc(c._id)}">${UI.esc(c.name)}</option>`).join(''));
    if (params.dataset) root.querySelector('#l-ds').value = params.dataset;
    ['#l-ds', '#l-cmp', '#l-tuned'].forEach(s => root.querySelector(s).onchange = fetchRows);
    ['#l-model', '#l-prompt'].forEach(s => root.querySelector(s).onchange = render);
    root.querySelector('#l-draft').onclick = draftForm;

    table = new Tabulator('#l-table', {
      layout: 'fitColumns', height: '50vh', placeholder: 'No misses match.', groupBy: false,
      columns: [
        { title: 'Repo', field: 'repo', width: 110, headerFilter: 'input' },
        { title: 'Model', field: 'model', width: 150, headerFilter: 'input', formatter: c => UI.esc(UI.shortModel(c.getValue())) },
        { title: 'Prompt', field: 'prompt', width: 95 },
        { title: 'Fact', field: 'fact', width: 140, headerFilter: 'input', formatter: c => `<span class="code">${UI.esc(c.getValue())}</span>` },
        { title: 'Category', field: 'category', width: 100, headerFilter: 'input' },
        { title: 'W', field: 'weight', width: 45, hozAlign: 'right' },
        { title: 'Cause', field: 'cause', width: 140, headerFilter: 'list', headerFilterParams: { values: Object.fromEntries([['', 'any'], ...CAUSES.map(c => [c, label(c)])]) }, formatter: c => ResultView.causeBadge(c.getValue()) },
        { title: 'Lesson', field: 'lesson', formatter: 'textarea', headerFilter: 'input' },
        { title: 'Evidence', field: 'evidence', formatter: 'textarea', widthGrow: 0.7 },
      ],
    });
    table.on('rowClick', (e, row) => ResultView.open(row.getData().rid));
    await fetchRows();
    renderSets();
  }

  async function fetchRows() {
    const q = new URLSearchParams();
    for (const [k, s] of [['dataset', '#l-ds'], ['campaign', '#l-cmp']]) { const v = document.querySelector(s).value; if (v) q.set(k, v); }
    if (document.getElementById('l-tuned').checked) q.set('exclude_tuned', '1');
    rows = (await UI.get('/api/results?' + q)).filter(r => r.delivered);
    const fill = (sel, vals) => { const el = document.querySelector(sel), prev = el.value; el.innerHTML = '<option value="">any</option>' + vals.map(v => `<option value="${UI.esc(v)}">${UI.esc(sel === '#l-model' ? UI.shortModel(v) : v)}</option>`).join(''); if (vals.includes(prev)) el.value = prev; };
    fill('#l-model', [...new Set(rows.map(r => r.model))].sort());
    fill('#l-prompt', [...new Set(rows.map(r => r.prompt_version || '?'))].sort());
    render();
  }

  function filtered() {
    const m = document.getElementById('l-model').value, p = document.getElementById('l-prompt').value;
    return rows.filter(r => (!m || r.model === m) && (!p || (r.prompt_version || '?') === p));
  }

  function render() {
    const R = filtered();
    misses = [];
    for (const r of R) {
      const d = new Map(((r.diagnosis && r.diagnosis.misses) || []).map(x => [x.id, x]));
      for (const m of r.misses || []) {
        const x = d.get(m.id);
        misses.push({ rid: r._id, repo: r.repo, model: r.model, prompt: r.prompt_version || '?', fact: m.id, category: m.category, weight: Number(m.weight || 1),
          cause: x ? x.cause : r.diagnosis ? 'unclear' : r.has_transcript ? 'undiagnosed' : 'no_transcript', lesson: x ? x.lesson : '', evidence: x ? x.evidence : '', fact_fix: x ? x.fact_fix : '' });
      }
    }
    table.replaceData(misses);
    const withMiss = R.filter(r => (r.misses || []).length), diagnosable = withMiss.filter(r => r.has_transcript && !r.diagnosis), noTx = withMiss.filter(r => !r.has_transcript);
    const diagnosed = withMiss.filter(r => r.diagnosis).length, weight = misses.reduce((a, m) => a + m.weight, 0);
    const bench = misses.filter(m => ResultView.BENCH.includes(m.cause)), classified = misses.filter(m => [...ResultView.PROBE, ...ResultView.BENCH].includes(m.cause));
    const kpi = (l, v, sub = '') => `<div class="card"><div class="kpi-l">${l}</div><div class="kpi">${v}</div><div class="text-xs text-slate-400">${sub}</div></div>`;
    document.getElementById('l-kpis').innerHTML = kpi('Scored runs', R.length) + kpi('Runs with misses', withMiss.length, `${misses.length} misses`) +
      kpi('Transcripts', `${withMiss.length - noTx.length}/${withMiss.length}`, 'of runs with misses') + kpi('Diagnosed', `${diagnosed}/${withMiss.length}`) +
      kpi('Weight missed / run', R.length ? (weight / R.length).toFixed(1) : '-') + kpi('Benchmark bugs', classified.length ? UI.pct(bench.length / classified.length) : '-', 'of diagnosed misses');

    const a = document.getElementById('l-actions');
    a.innerHTML = `<button id="l-diag" class="btn btn-primary" ${diagnosable.length ? '' : 'disabled'}>Diagnose ${diagnosable.length} run${diagnosable.length === 1 ? '' : 's'}</button>
      <span>with</span><select id="l-dmodel" class="inp">${hosted.map(h => `<option>${UI.esc(h)}</option>`).join('')}</select>
      <span class="text-slate-400">${noTx.length ? `${noTx.length} runs with misses have no stored transcript: run <span class="code">agent\\eval\\worker\\Push-Transcripts.ps1</span> on the host (runs before transcripts were stored, or whose transcript two probes shared, cannot be diagnosed).` : 'Every run with misses has a transcript.'}</span>`;
    a.querySelector('#l-diag').onclick = async () => {
      const r = await UI.act(() => UI.post('/api/diagnose', { ids: diagnosable.map(x => x._id), model: a.querySelector('#l-dmodel').value }));
      UI.toast(`Queued ${r.queued} diagnoses${r.matched > r.queued ? ` (${r.matched - r.queued} already queued)` : ''}`);
    };

    const cats = [...new Set(misses.map(m => m.category))].sort((x, y) => misses.filter(m => m.category === y).reduce((s, m) => s + m.weight, 0) - misses.filter(m => m.category === x).reduce((s, m) => s + m.weight, 0));
    const present = CAUSES.filter(c => misses.some(m => m.cause === c));
    Plotly.react('l-cat', present.map(c => ({ type: 'bar', name: label(c), marker: { color: COLORS[c] }, x: cats, y: cats.map(k => misses.filter(m => m.category === k && m.cause === c).reduce((s, m) => s + m.weight, 0) / Math.max(1, R.length)), hovertemplate: `${label(c)}: %{y:.2f}<extra></extra>` })),
      { ...PLOT, barmode: 'stack', yaxis: { title: 'weight per run', gridcolor: '#1e293b' }, legend: { orientation: 'h', y: -0.3 } }, CFG);
    const groups = [...new Set(R.map(r => `${UI.shortModel(r.model)} · ${r.prompt_version || '?'}`))];
    const runsIn = g => R.filter(r => `${UI.shortModel(r.model)} · ${r.prompt_version || '?'}` === g).length;
    Plotly.react('l-model-plot', present.map(c => ({ type: 'bar', name: label(c), marker: { color: COLORS[c] }, x: groups, y: groups.map(g => misses.filter(m => `${UI.shortModel(m.model)} · ${m.prompt}` === g && m.cause === c).reduce((s, m) => s + m.weight, 0) / Math.max(1, runsIn(g))), hovertemplate: `${label(c)}: %{y:.2f}<extra></extra>` })),
      { ...PLOT, barmode: 'stack', yaxis: { title: 'weight per run', gridcolor: '#1e293b' }, xaxis: { automargin: true }, legend: { orientation: 'h', y: -0.35 } }, CFG);

    const issues = new Map();
    for (const m of bench) { const k = `${m.repo}/${m.fact}`; if (!issues.has(k)) issues.set(k, { ...m, n: 0, fixes: new Set() }); const i = issues.get(k); i.n++; if (m.fact_fix) i.fixes.add(m.fact_fix); }
    document.getElementById('l-bench').innerHTML = issues.size ? `<table class="simple"><tr><th>Fact</th><th>Cause</th><th>Runs</th><th>Suggested fix</th><th></th></tr>${[...issues.values()].sort((x, y) => y.n * y.weight - x.n * x.weight).map(i => `<tr>
        <td class="code">${UI.esc(i.repo)}/${UI.esc(i.fact)}</td><td>${ResultView.causeBadge(i.cause)}</td><td>${i.n}</td><td class="code text-xs">${[...i.fixes].map(UI.esc).join('<br>')}</td>
        <td>${benchToRepo.has(i.repo) ? `<a class="btn btn-xs" href="#repos?id=${encodeURIComponent(benchToRepo.get(i.repo))}">Edit facts</a>` : ''}</td></tr>`).join('')}</table>`
      : '<div class="text-slate-400 text-sm">None found among diagnosed misses.</div>';
  }

  function renderSets() {
    const el = document.getElementById('l-sets');
    el.innerHTML = sets.length ? `<table class="simple"><tr><th>Set</th><th>Status</th><th>Items</th><th>From</th><th>Runs</th><th>Created</th></tr>${sets.map(L => {
      const acc = (L.items || []).filter(i => i.accepted);
      return `<tr class="cursor-pointer hover:bg-slate-800" data-set="${UI.esc(L._id)}"><td><b>${UI.esc(L._id)}</b> ${UI.esc(L.name !== L._id ? L.name : '')}</td><td>${UI.badge(L.status)}</td>
        <td>${acc.filter(i => i.kind === 'prompt').length} prompt, ${acc.filter(i => i.kind === 'tool').length} tool${(L.items || []).length > acc.length ? ` <span class="text-slate-500">(+${L.items.length - acc.length} not accepted)</span>` : ''}</td>
        <td>${(L.source.datasets || []).map(id => UI.esc((dsList.find(d => d._id === id) || { name: id }).name)).join(', ')}${L.parent ? ` · from ${UI.esc(L.parent)}` : ''}</td><td>${L.source.results || ''}</td><td>${UI.ago(L.created_at)}</td></tr>`;
    }).join('')}</table>` : '<div class="text-slate-400 text-sm">No lesson sets yet. Diagnose runs on a tuning dataset, then draft one.</div>';
    el.querySelectorAll('[data-set]').forEach(tr => tr.onclick = () => openSet(tr.dataset.set));
  }
  async function reloadSets() { sets = await UI.get('/api/lessons'); renderSets(); }

  function draftForm() {
    const diagnosedModels = [...new Set(rows.filter(r => r.diagnosis).map(r => r.model))].sort();
    const body = UI.openDrawer('Draft a lesson set', `
      <div class="text-sm text-slate-300 mb-3">The drafting model reads every diagnosed miss on the chosen datasets and proposes general habits for the probe, each backed by misses on at least two repos. Nothing reaches the probe until you review and activate the set.</div>
      <label class="lbl">Name</label><input id="f-name" class="inp w-full mb-3" placeholder="e.g. systemd and config habits">
      <label class="lbl">Datasets to learn from (held-out datasets are locked out)</label>
      <div class="grid grid-cols-2 gap-1 text-sm mb-3">${dsList.map(d => `<label class="flex items-center gap-2 ${d.role === 'heldout' ? 'opacity-40' : ''}"><input type="checkbox" value="${UI.esc(d._id)}" ${d.role === 'heldout' ? 'disabled' : d.role === 'tuning' ? 'checked' : ''} data-ds>${UI.esc(d.name)} ${d.role ? `<span class="text-xs text-slate-400">${d.role === 'heldout' ? 'held-out' : 'tuning'}</span>` : ''}</label>`).join('')}</div>
      <label class="lbl">Only runs by these models (none checked: all)</label>
      <div class="grid grid-cols-2 gap-1 text-sm mb-3">${diagnosedModels.map(m => `<label class="flex items-center gap-2"><input type="checkbox" value="${UI.esc(m)}" data-m>${UI.esc(UI.shortModel(m))}</label>`).join('') || '<span class="text-slate-400">No diagnosed runs in the current filter.</span>'}</div>
      <div class="grid grid-cols-2 gap-3 mb-4">
        <div><label class="lbl">Build on (keeps its accepted items where still supported)</label><select id="f-parent" class="inp w-full"><option value="">nothing</option>${sets.filter(L => ['active', 'draft', 'retired'].includes(L.status)).map(L => `<option value="${UI.esc(L._id)}">${UI.esc(L._id)}: ${UI.esc(L.name)} (${L.status})</option>`).join('')}</select></div>
        <div><label class="lbl">Drafting model</label><select id="f-model" class="inp w-full">${hosted.map(h => `<option>${UI.esc(h)}</option>`).join('')}</select></div></div>
      <button id="f-go" class="btn btn-primary">Queue drafting</button>`);
    body.querySelector('#f-go').onclick = async () => {
      const b = { name: body.querySelector('#f-name').value, datasets: [...body.querySelectorAll('[data-ds]:checked')].map(i => i.value), models: [...body.querySelectorAll('[data-m]:checked')].map(i => i.value),
        parent: body.querySelector('#f-parent').value || null, model: body.querySelector('#f-model').value };
      const L = await UI.act(() => UI.post('/api/lessons', b));
      UI.toast(`${L._id} queued from ${L.source.results} diagnosed runs`);
      await reloadSets(); openSet(L._id);
    };
  }

  async function openSet(id) {
    const L = await UI.get('/api/lesson?id=' + encodeURIComponent(id));
    const edit = L.status === 'draft';
    const heldout = dsList.filter(d => d.role === 'heldout');
    const body = UI.openDrawer(`Lesson set ${L._id}`, `
      <div class="flex flex-wrap items-center gap-2 mb-3">
        <input id="s-name" class="inp flex-1 min-w-[240px]" value="${UI.esc(L.name)}" ${edit ? '' : 'disabled'}> ${UI.badge(L.status)}
        ${edit ? '<button id="s-save" class="btn">Save</button><button id="s-act" class="btn btn-primary">Activate</button>' : ''}
        ${['draft', 'failed'].includes(L.status) ? '<button id="s-redraft" class="btn">Redraft</button>' : ''}
        ${L.status === 'active' ? `<a class="btn btn-primary" href="#launch?guidance=${encodeURIComponent(L._id)}${heldout[0] ? `&dataset=${encodeURIComponent(heldout[0]._id)}` : ''}">Run on ${heldout[0] ? UI.esc(heldout[0].name) : 'a held-out dataset'}</a><button id="s-retire" class="btn">Retire</button>` : ''}
        ${L.status !== 'drafting' ? '<button id="s-fork" class="btn">Fork as new draft</button>' : ''}
        ${L.used_by ? '' : '<button id="s-del" class="btn btn-danger">Delete</button>'}</div>
      <div class="text-xs text-slate-400 mb-3">From ${L.source.results} diagnosed runs on ${(L.source.datasets || []).map(x => UI.esc((dsList.find(d => d._id === x) || { name: x }).name)).join(', ')}${(L.source.models || []).length ? ` (models: ${L.source.models.map(m => UI.esc(UI.shortModel(m))).join(', ')})` : ''}; drafted by ${UI.esc(UI.shortModel(L.model))}${L.parent ? `, building on ${UI.esc(L.parent)}` : ''}. ${L.used_by ? `Used by ${L.used_by} campaigns.` : ''}
        Probe runs with it report prompt version <span class="code">probe-5+${UI.esc(L._id)}</span>.</div>
      ${L.status === 'drafting' ? `<div class="card text-sm">Drafting (job ${UI.esc(L.job || '')}). It appears in the queue; reopen this set when it finishes.</div>` : ''}
      ${L.status === 'failed' ? `<div class="card text-sm text-red-300">Drafting failed: ${UI.esc(L.error || '')}</div>` : ''}
      ${L.notes ? `<div class="card mb-3 text-sm">${UI.esc(L.notes)}</div>` : ''}
      <div id="s-items" class="flex flex-col gap-2 mb-3"></div>
      ${edit ? '<button id="s-add" class="btn btn-xs mb-3">Add item</button>' : ''}
      <div class="card"><h2>What the probe gets (accepted prompt items)</h2><div id="s-preview" class="log"></div>
        <div class="text-xs text-slate-400 mt-2">Accepted tool items are changes to make in Probe.ps1 or its inventory, not prompt text.</div></div>`);
    let items = (L.items || []).map(i => ({ ...i }));
    const itemsEl = body.querySelector('#s-items');
    const preview = () => { body.querySelector('#s-preview').textContent = items.filter(i => i.accepted && i.kind === 'prompt').map(i => `- ${i.text}`).join('\n') || '(nothing accepted yet)'; };
    const draw = () => {
      itemsEl.innerHTML = items.map((i, k) => `<div class="card ${i.accepted ? '' : 'opacity-60'}" data-k="${k}">
        <div class="flex items-start gap-2">
          <input type="checkbox" data-f="accepted" ${i.accepted ? 'checked' : ''} ${edit ? '' : 'disabled'} class="mt-1" title="accept">
          <select data-f="kind" class="inp" ${edit ? '' : 'disabled'}><option value="prompt" ${i.kind === 'prompt' ? 'selected' : ''}>prompt</option><option value="tool" ${i.kind === 'tool' ? 'selected' : ''}>tool</option></select>
          <textarea data-f="text" class="inp flex-1 text-sm" rows="2" ${edit ? '' : 'disabled'}>${UI.esc(i.text)}</textarea>
          ${edit ? `<button class="btn btn-xs" data-rm="${k}">Remove</button>` : ''}</div>
        <div class="text-xs text-slate-400 mt-1 ml-6">${(i.categories || []).map(c => `<span class="badge b-metadata mr-1">${UI.esc(c)}</span>`).join('')}
          backed by ${(i.repos || []).length} repo${(i.repos || []).length === 1 ? '' : 's'}: ${(i.repos || []).map(UI.esc).join(', ')}${i.weak ? ' <span class="text-amber-300">(fewer than two repos: may not generalize)</span>' : ''}
          ${i.rationale ? `<div class="mt-0.5">${UI.esc(i.rationale)}</div>` : ''}</div></div>`).join('') || (L.status === 'drafting' ? '' : '<div class="text-slate-400 text-sm">No items.</div>');
      itemsEl.querySelectorAll('[data-k]').forEach(card => {
        const k = Number(card.dataset.k);
        card.querySelectorAll('[data-f]').forEach(inp => inp.oninput = inp.onchange = () => { const f = inp.dataset.f; items[k][f] = f === 'accepted' ? inp.checked : inp.value; card.classList.toggle('opacity-60', !items[k].accepted); preview(); });
      });
      itemsEl.querySelectorAll('[data-rm]').forEach(b => b.onclick = () => { items.splice(Number(b.dataset.rm), 1); draw(); });
      preview();
    };
    draw();
    const on = (sel, fn) => { const b = body.querySelector(sel); if (b) b.onclick = fn; };
    const save = () => UI.put('/api/lesson?id=' + encodeURIComponent(L._id), { name: body.querySelector('#s-name').value, items });
    const act = action => UI.post('/api/lesson/action', { id: L._id, action });
    on('#s-add', () => { items.push({ text: '', kind: 'prompt', categories: [], repos: [], accepted: true }); draw(); });
    on('#s-save', async () => { await UI.act(save, 'Saved'); await reloadSets(); });
    on('#s-act', async () => { await UI.act(save); await UI.act(() => act('activate'), `${L._id} is active: pick it as guidance in New eval`); await reloadSets(); openSet(L._id); });
    on('#s-redraft', async () => { await UI.act(() => act('redraft'), 'Redrafting'); await reloadSets(); openSet(L._id); });
    on('#s-retire', async () => { await UI.act(() => act('retire'), 'Retired'); await reloadSets(); openSet(L._id); });
    on('#s-fork', async () => { const F = await UI.act(() => act('fork')); await reloadSets(); openSet(F._id); });
    on('#s-del', async () => { if (!confirm(`Delete ${L._id}?`)) return; await UI.act(() => act('delete'), 'Deleted'); UI.closeDrawer(); await reloadSets(); });
  }

  return { mount, unmount() { if (table) table.destroy(); table = null; } };
})());
