// Trajectories: a probe run as the sequence of turns the model took.
// Each turn is the assistant message, its tool calls, and the outputs those
// calls returned. A diagnosis that names a turn ("Turn 2: ...") marks it.
UI.register('trajectories', (() => {
  let table, rows = [], selected = null;

  async function mount(root, params) {
    root.innerHTML = `
      <div class="card mb-4 flex flex-wrap gap-3 items-end">
        <div><label class="lbl">Model</label><select id="tr-model" class="inp"><option value="">any</option></select></div>
        <div class="flex-1 min-w-[200px]"><label class="lbl">Repo</label><input id="tr-repo" class="inp w-full" placeholder="filter by repo"></div>
        <span id="tr-count" class="text-xs text-slate-400 pb-1"></span>
      </div>
      <div class="grid grid-cols-1 xl:grid-cols-[420px_1fr] gap-4 items-start">
        <div id="tr-runs"></div>
        <div class="card" id="tr-view"><div class="text-slate-400 text-sm">Select a run. Each row is one probe: its tool calls and the outputs it saw, in order.</div></div>
      </div>`;
    root.querySelector('#tr-model').onchange = applyFilters;
    root.querySelector('#tr-repo').oninput = applyFilters;
    table = new Tabulator('#tr-runs', {
      layout: 'fitColumns', height: '74vh', index: '_id', selectableRows: 1, placeholder: 'No trajectories stored.',
      columns: [
        { title: 'Repo', field: 'repo', width: 120 },
        { title: 'Model', field: 'model', formatter: c => UI.esc(UI.shortModel(c.getValue())) },
        { title: 'Recall', field: 'recall', width: 70, hozAlign: 'right', formatter: c => UI.pct(c.getValue()) },
        { title: 'Turns', field: 'turns', width: 64, hozAlign: 'right' },
        { title: 'When', field: 'run_at', width: 88, formatter: c => UI.ago(c.getValue()) },
      ],
    });
    table.on('rowClick', (e, row) => openRun(row.getData()._id));
    const all = await UI.get('/api/results?limit=5000');
    rows = all.filter(r => r.has_transcript).map(r => ({ ...r, turns: r.probe && r.probe.stats ? r.probe.stats.model_turns : null }));
    const models = [...new Set(rows.map(r => r.model))].sort();
    root.querySelector('#tr-model').insertAdjacentHTML('beforeend', models.map(m => `<option value="${UI.esc(m)}">${UI.esc(UI.shortModel(m))}</option>`).join(''));
    await applyFilters();
    if (params.id) openRun(params.id);
  }

  function shown() {
    const m = document.getElementById('tr-model').value;
    const q = document.getElementById('tr-repo').value.trim().toLowerCase();
    return rows.filter(r => (!m || r.model === m) && (!q || String(r.repo || '').toLowerCase().includes(q)));
  }

  let filterGen = 0;
  async function applyFilters() {
    const gen = ++filterGen;
    const list = shown();
    await table.setData(list);
    if (gen !== filterGen) return;
    const count = document.getElementById('tr-count');
    if (count) count.textContent = `${list.length} trajectories`;
    if (selected && list.some(r => r._id === selected)) table.selectRow(selected);
  }

  async function openRun(id) {
    selected = id;
    const url = '#trajectories?id=' + encodeURIComponent(id);
    if (location.hash !== url) history.replaceState(null, '', url);
    if (table.getRow(id)) table.selectRow(id);
    const view = document.getElementById('tr-view');
    const r = rows.find(x => x._id === id);
    if (!r) { view.innerHTML = '<div class="text-slate-400 text-sm">No trajectory stored for that run.</div>'; return; }
    const st = (r.probe && r.probe.stats) || {};
    view.innerHTML = `
      <div class="flex flex-wrap items-baseline gap-x-4 gap-y-1 mb-3 text-sm">
        <b>${UI.esc(r.repo)}</b>
        <span>${UI.esc(UI.shortModel(r.model))}</span>
        <span>recall <b>${r.delivered ? UI.pct(r.recall) : 'n/a'}</b></span>
        <span class="text-slate-400">${st.model_turns || '-'} turns · ${st.tool_calls || '-'} tools · ${UI.dur(st.seconds)} · ${UI.esc(r.stop_reason || '')}</span>
        <button type="button" class="tr-more text-sky-400 text-xs">misses and document</button>
      </div>
      <div id="tr-body"><div class="text-slate-400 text-sm">Loading...</div></div>`;
    view.querySelector('.tr-more').onclick = () => ResultView.open(id);
    const t = await UI.get('/api/transcript?id=' + encodeURIComponent(id));
    const body = document.getElementById('tr-body');
    if (selected !== id || !body) return;
    Trajectory.show(body, r, (t && t.events) || []);
  }

  return { mount, unmount() { if (table) table.destroy(); table = null; } };
})());

// Shared by this page and the result drawer.
window.Trajectory = (() => {
  function group(events) {
    const steps = [];
    let turn = null, n = 0;
    for (const e of events || []) {
      if (e.t === 'start') { steps.push({ start: e }); turn = null; continue; }
      if (e.t === 'assistant') { turn = { n: ++n, assistant: e, tools: [], errors: [] }; steps.push(turn); continue; }
      if (!turn) { turn = { n: ++n, assistant: null, tools: [], errors: [] }; steps.push(turn); }
      if (e.t === 'tool') turn.tools.push(e);
      else if (e.t === 'error') turn.errors.push(e);
    }
    return steps;
  }

  // Diagnosis evidence is written as "Turn 4: ...". A miss can name more than one turn.
  function citedTurns(result) {
    const map = new Map();
    for (const m of ((result.diagnosis && result.diagnosis.misses) || [])) {
      const re = /turn\s+(\d+)/gi;
      let x;
      while ((x = re.exec(String(m.evidence || '')))) {
        const n = Number(x[1]);
        if (!map.has(n)) map.set(n, []);
        if (!map.get(n).some(y => y.id === m.id)) map.get(n).push(m);
      }
    }
    return map;
  }

  function callHtml(call, tool) {
    const args = String((call && call.args) || '');
    const name = UI.esc((call && call.name) || (tool && tool.name) || '');
    const long = args.length > 280 || (call && call.name) === 'write_install_md';
    const argsHtml = !args ? '' : long
      ? `<details><summary class="text-xs text-emerald-300 cursor-pointer">arguments, ${args.length} characters</summary><div class="code text-emerald-300 break-all whitespace-pre-wrap mt-1">${UI.esc(args)}</div></details>`
      : `<div class="code text-emerald-300 break-all whitespace-pre-wrap">${UI.esc(args)}</div>`;
    const out = tool ? `<details class="mt-1"><summary class="text-xs text-slate-400 cursor-pointer">← ${UI.esc(String(tool.out || '').split('\n')[0].slice(0, 140)) || '(empty)'}</summary><div class="log">${UI.esc(tool.out)}</div></details>` : '';
    return `<div class="ml-1 mb-2"><div class="text-xs text-emerald-300">→ ${name}</div>${argsHtml}${out}</div>`;
  }

  function show(el, result, events) {
    const steps = group(events);
    const cited = citedTurns(result);
    const turns = steps.filter(s => s.n).length;
    el.innerHTML = `
      <div class="flex gap-2 mb-2">
        <input class="tr-q inp flex-1" placeholder="filter turns: reasoning, commands, outputs, evidence">
        <span class="tr-n text-xs text-slate-400 self-center whitespace-nowrap"></span>
      </div>
      <div class="tr-steps"></div>`;
    const blocks = steps.map(s => {
      if (s.start) return { text: `${s.start.system} ${s.start.user}`, html: `<details class="mb-3"><summary class="text-xs text-slate-400 cursor-pointer">System prompt and task (${UI.esc(s.start.model || '')})</summary><div class="log mt-1">${UI.esc(s.start.system)}\n\n--- task ---\n${UI.esc(s.start.user)}</div></details>` };
      const marks = cited.get(s.n) || [];
      const calls = (s.assistant && s.assistant.calls) || [];
      const text = [s.assistant && s.assistant.reasoning, s.assistant && s.assistant.text, ...calls.map(c => `${c.name} ${c.args}`), ...s.tools.map(t => `${t.name} ${t.out}`), ...marks.map(m => `${m.id} ${m.evidence} ${m.lesson}`)].join(' ');
      const paired = calls.map((c, i) => callHtml(c, s.tools[i]));
      const extra = s.tools.slice(calls.length).map(t => callHtml({ name: t.name, args: t.args }, t));
      return { text, html: `<div class="turn${marks.length ? ' cited' : ''}">
        <div class="text-xs mb-1"><span class="text-sky-300 font-semibold">turn ${s.n}</span>
          ${marks.map(m => `${ResultView.causeBadge(m.cause)} <span class="code text-slate-300">${UI.esc(m.id)}</span>`).join(' ')}</div>
        ${marks.map(m => `<div class="text-xs text-amber-200/90 mb-1">${UI.esc(m.evidence || '')}${m.lesson ? `<div class="text-slate-400">${UI.esc(m.lesson)}</div>` : ''}</div>`).join('')}
        ${s.assistant && s.assistant.reasoning ? `<div class="text-xs text-slate-400 italic whitespace-pre-wrap">${UI.esc(s.assistant.reasoning)}</div>` : ''}
        ${s.assistant && s.assistant.text ? `<div class="text-sm whitespace-pre-wrap mb-1">${UI.esc(s.assistant.text)}</div>` : ''}
        ${paired.join('')}${extra.join('')}
        ${s.errors.map(e => `<div class="text-xs text-red-300 ml-4">server error: ${UI.esc(e.text)}</div>`).join('')}
      </div>` };
    });
    const render = () => {
      const q = el.querySelector('.tr-q').value.trim().toLowerCase();
      const hit = q ? blocks.filter(b => String(b.text).toLowerCase().includes(q)) : blocks;
      el.querySelector('.tr-steps').innerHTML = hit.map(b => b.html).join('') || '<div class="text-slate-400 text-sm">No turns match.</div>';
      if (q) el.querySelectorAll('details').forEach(d => d.open = true);
      el.querySelector('.tr-n').textContent = q ? `${hit.length} of ${blocks.length}` : `${turns} turns`;
    };
    el.querySelector('.tr-q').oninput = render;
    render();
    const first = el.querySelector('.turn.cited');
    if (first) first.scrollIntoView({ block: 'nearest' });
  }

  return { show };
})();
