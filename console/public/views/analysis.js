// Analysis: compare models (or model + prompt version) on scored runs.
// Recall is averaged per repo first (macro), so a model is not rewarded for
// having more runs on easy repos; head-to-head uses only repos both have.
UI.register('analysis', (() => {
  let rows = [], runsTable;
  const PLOT = { paper_bgcolor: 'rgba(0,0,0,0)', plot_bgcolor: 'rgba(0,0,0,0)', font: { color: '#cbd5e1', size: 11 }, margin: { l: 50, r: 10, t: 10, b: 40 } };
  const CFG = { displayModeBar: false, responsive: true };

  async function mount(root, params) {
    root.innerHTML = `
      <div class="card mb-4 flex flex-wrap gap-3 items-end">
        <div><label class="lbl">Dataset</label><select id="a-ds" class="inp"><option value="">all repos</option></select></div>
        <div><label class="lbl">Campaign</label><select id="a-cmp" class="inp"><option value="">any</option></select></div>
        <div><label class="lbl">Environment</label><select id="a-env" class="inp"><option value="">any</option><option value="wsl">wsl</option><option value="local">local</option></select></div>
        <label class="flex items-center gap-1 text-sm pb-1" title="Runs with lesson-set guidance on repos the lessons were drafted from measure memorization, not generalization"><input id="a-tuned" type="checkbox" checked> exclude repos the guidance was tuned on</label>
        <div><label class="lbl">Compare by</label><select id="a-group" class="inp"><option value="model">model</option><option value="model_prompt">model + prompt version</option><option value="prompt">prompt version</option></select></div>
        <div class="flex-1 min-w-[300px]"><label class="lbl">Include</label><div id="a-groups" class="flex flex-wrap gap-x-3 gap-y-1 text-sm"></div></div>
      </div>
      <div class="grid grid-cols-1 xl:grid-cols-2 gap-4">
        <div class="card xl:col-span-2"><h2>Leaderboard</h2><div id="a-board"></div></div>
        <div class="card"><h2>Recall (macro over repos, 95% CI)</h2><div id="a-bar" style="height:300px"></div></div>
        <div class="card"><h2>Time vs recall</h2><div id="a-scatter" style="height:300px"></div></div>
        <div class="card xl:col-span-2"><h2>Recall by repo</h2><div id="a-heat" style="height:420px"></div></div>
        <div class="card"><h2>Weighted misses per run, by category</h2><div id="a-cat" style="height:320px"></div></div>
        <div class="card"><h2>Head to head</h2>
          <div class="flex gap-2 mb-2 text-sm"><select id="h-a" class="inp flex-1"></select><span class="self-center">vs</span><select id="h-b" class="inp flex-1"></select></div>
          <div id="h-sum" class="text-sm mb-2"></div><div id="h-plot" style="height:240px"></div></div>
        <div class="card xl:col-span-2"><h2>Most-missed facts</h2><div id="a-facts" class="overflow-auto" style="max-height:420px"></div></div>
        <div class="card xl:col-span-2"><h2>Runs</h2><div id="a-runs"></div></div>
      </div>`;
    const [ds, cs] = await Promise.all([UI.get('/api/datasets'), UI.get('/api/campaigns')]);
    root.querySelector('#a-ds').insertAdjacentHTML('beforeend', ds.map(d => `<option value="${UI.esc(d._id)}">${UI.esc(d.name)}</option>`).join(''));
    root.querySelector('#a-cmp').insertAdjacentHTML('beforeend', cs.map(c => `<option value="${UI.esc(c._id)}">${UI.esc(c.name)}</option>`).join(''));
    if (params.dataset) root.querySelector('#a-ds').value = params.dataset;
    if (params.campaign) root.querySelector('#a-cmp').value = params.campaign;
    ['#a-ds', '#a-cmp', '#a-env', '#a-tuned'].forEach(s => root.querySelector(s).onchange = fetchRows);
    root.querySelector('#a-group').onchange = () => { buildGroupPicker(); render(); };
    root.querySelector('#h-a').onchange = headToHead; root.querySelector('#h-b').onchange = headToHead;

    runsTable = new Tabulator('#a-runs', {
      layout: 'fitColumns', height: '40vh', placeholder: 'No runs match.',
      columns: [
        { title: 'When', field: 'run_at', width: 95, formatter: c => UI.ago(c.getValue()), sorter: 'datetime' },
        { title: 'Repo', field: 'repo', width: 120, headerFilter: 'input' },
        { title: 'Model', field: 'model', width: 200, headerFilter: 'input', formatter: c => UI.esc(UI.shortModel(c.getValue())) },
        { title: 'Prompt', field: 'prompt_version', width: 80 },
        { title: 'Env', field: 'target_env', width: 60 },
        { title: 'Recall', field: 'recall', width: 75, hozAlign: 'right', formatter: c => c.getRow().getData().delivered ? UI.pct(c.getValue()) : '<span class="text-amber-300">none</span>' },
        { title: 'Facts', field: 'facts_hit', width: 65, formatter: c => { const d = c.getRow().getData(); return d.delivered ? `${d.facts_hit}/${d.facts}` : ''; } },
        { title: 'Checks', field: 'live_checks', width: 70, formatter: c => { const l = c.getValue(); return l ? `${l.passed}/${(l.passed || 0) + (l.failed || 0) + (l.untested || 0)}` : ''; } },
        { title: 'Turns', field: 'probe.stats.model_turns', width: 65, hozAlign: 'right' },
        { title: 'Time', field: 'probe.stats.seconds', width: 75, formatter: c => UI.dur(c.getValue()) },
        { title: 'Stop', field: 'stop_reason', width: 100 },
        { title: 'Missed', field: 'misses', formatter: c => `<span class="text-xs text-red-300">${(c.getValue() || []).map(m => UI.esc(m.id)).join(', ')}</span>` },
      ],
    });
    runsTable.on('rowClick', (e, row) => openResult(row.getData()._id));
    await fetchRows();
    if (params.result) openResult(params.result);
  }

  async function fetchRows() {
    const q = new URLSearchParams();
    for (const [k, s] of [['dataset', '#a-ds'], ['campaign', '#a-cmp'], ['target_env', '#a-env']]) { const v = document.querySelector(s).value; if (v) q.set(k, v); }
    if (document.getElementById('a-tuned').checked) q.set('exclude_tuned', '1');
    rows = await UI.get('/api/results?' + q);
    buildGroupPicker(); render();
  }

  const groupOf = r => { const g = document.getElementById('a-group').value; return g === 'prompt' ? (r.prompt_version || '?') : g === 'model_prompt' ? `${UI.shortModel(r.model)} · ${r.prompt_version || '?'}` : UI.shortModel(r.model); };
  function buildGroupPicker() {
    const counts = {}; rows.forEach(r => { const g = groupOf(r); counts[g] = (counts[g] || 0) + 1; });
    const prev = new Set([...document.querySelectorAll('#a-groups input:not(:checked)')].map(i => i.value));
    document.getElementById('a-groups').innerHTML = Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([g, n]) =>
      `<label class="flex items-center gap-1"><input type="checkbox" value="${UI.esc(g)}" ${prev.has(g) ? '' : 'checked'}> ${UI.esc(g)} <span class="text-slate-500">(${n})</span></label>`).join('');
    document.querySelectorAll('#a-groups input').forEach(i => i.onchange = render);
  }
  const included = () => new Set([...document.querySelectorAll('#a-groups input:checked')].map(i => i.value));

  const mean = a => a.length ? a.reduce((x, y) => x + y, 0) / a.length : null;
  const median = a => { if (!a.length) return null; const s = [...a].sort((x, y) => x - y), m = Math.floor(s.length / 2); return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2; };
  function ci95(vals) { if (vals.length < 2) return 0; const m = mean(vals), sd = Math.sqrt(vals.reduce((a, v) => a + (v - m) ** 2, 0) / (vals.length - 1)); return 1.96 * sd / Math.sqrt(vals.length); }

  function stats() {
    const inc = included(), groups = new Map();
    for (const r of rows) {
      const g = groupOf(r); if (!inc.has(g)) continue;
      if (!groups.has(g)) groups.set(g, { name: g, runs: [], byRepo: new Map() });
      const G = groups.get(g); G.runs.push(r);
      if (r.delivered) { if (!G.byRepo.has(r.repo)) G.byRepo.set(r.repo, []); G.byRepo.get(r.repo).push(r.recall); }
    }
    for (const G of groups.values()) {
      const delivered = G.runs.filter(r => r.delivered);
      G.repoMeans = new Map([...G.byRepo].map(([k, v]) => [k, mean(v)]));
      const rm = [...G.repoMeans.values()];
      G.macro = mean(rm); G.ci = ci95(rm); G.repos = rm.length;
      G.deliveredRate = G.runs.length ? delivered.length / G.runs.length : 0;
      const lc = delivered.filter(r => r.live_checks); const pass = lc.reduce((a, r) => a + (r.live_checks.passed || 0), 0), tot = lc.reduce((a, r) => a + (r.live_checks.passed || 0) + (r.live_checks.failed || 0), 0);
      G.checkRate = tot ? pass / tot : null;
      G.turns = median(G.runs.map(r => r.probe && r.probe.stats && r.probe.stats.model_turns).filter(x => x != null));
      G.seconds = mean(G.runs.map(r => r.probe && r.probe.stats && r.probe.stats.seconds).filter(x => x != null));
      G.formatProblems = mean(delivered.map(r => (r.format_problems || []).length));
      G.catMiss = {}; for (const r of delivered) for (const m of r.misses || []) G.catMiss[m.category] = (G.catMiss[m.category] || 0) + Number(m.weight || 1) / delivered.length;
    }
    return [...groups.values()].sort((a, b) => (b.macro || 0) - (a.macro || 0));
  }

  function render() {
    const S = stats();
    runsTable.replaceData(rows.filter(r => included().has(groupOf(r))));
    document.getElementById('a-board').innerHTML = S.length ? `<table class="simple"><tr><th>#</th><th>${UI.esc(document.getElementById('a-group').selectedOptions[0].text)}</th><th>Recall (macro)</th><th>±95%</th><th>Repos</th><th>Runs</th><th>Delivered</th><th>Live checks passed</th><th>Median turns</th><th>Mean time</th><th>Format problems</th></tr>` +
      S.map((G, i) => `<tr><td>${i + 1}</td><td><b>${UI.esc(G.name)}</b></td><td><div class="flex items-center gap-2"><div class="bar w-28"><div style="width:${Math.round((G.macro || 0) * 100)}%"></div></div><b>${UI.pct(G.macro)}</b></div></td>
        <td>${UI.pct(G.ci)}</td><td>${G.repos}</td><td>${G.runs.length}</td><td>${UI.pct(G.deliveredRate)}</td><td>${UI.pct(G.checkRate)}</td><td>${G.turns ?? ''}</td><td>${UI.dur(G.seconds)}</td><td>${G.formatProblems != null ? G.formatProblems.toFixed(1) : ''}</td></tr>`).join('') + '</table>'
      : '<div class="text-slate-400 text-sm">No scored runs match these filters.</div>';

    Plotly.react('a-bar', [{ type: 'bar', x: S.map(G => G.name), y: S.map(G => G.macro), error_y: { type: 'data', array: S.map(G => G.ci), visible: true, color: '#94a3b8' }, marker: { color: '#0ea5e9' }, hovertemplate: '%{x}<br>%{y:.0%}<extra></extra>' }],
      { ...PLOT, yaxis: { range: [0, 1], tickformat: '.0%', gridcolor: '#1e293b' }, xaxis: { automargin: true } }, CFG);
    Plotly.react('a-scatter', [{ type: 'scatter', mode: 'markers+text', x: S.map(G => (G.seconds || 0) / 60), y: S.map(G => G.macro), text: S.map(G => G.name), textposition: 'top center', marker: { size: S.map(G => 8 + Math.sqrt(G.runs.length) * 3), color: '#38bdf8' }, hovertemplate: '%{text}<br>%{x:.1f} min, %{y:.0%}<extra></extra>' }],
      { ...PLOT, xaxis: { title: 'mean minutes per run', gridcolor: '#1e293b' }, yaxis: { range: [0, 1.05], tickformat: '.0%', gridcolor: '#1e293b' } }, CFG);

    const repos = [...new Set(S.flatMap(G => [...G.repoMeans.keys()]))].sort();
    Plotly.react('a-heat', [{ type: 'heatmap', x: S.map(G => G.name), y: repos, z: repos.map(r => S.map(G => G.repoMeans.has(r) ? G.repoMeans.get(r) : null)), zmin: 0, zmax: 1, colorscale: [[0, '#7f1d1d'], [0.5, '#a16207'], [1, '#15803d']],
      text: repos.map(r => S.map(G => G.repoMeans.has(r) ? `${Math.round(G.repoMeans.get(r) * 100)}% (${G.byRepo.get(r).length})` : '')), texttemplate: '%{text}', hovertemplate: '%{y} · %{x}<br>%{text}<extra></extra>', colorbar: { tickformat: '.0%' } }],
      { ...PLOT, margin: { l: 110, r: 10, t: 10, b: 60 }, height: Math.max(260, 26 * repos.length + 90) }, CFG);
    document.getElementById('a-heat').style.height = Math.max(260, 26 * repos.length + 90) + 'px';

    const cats = [...new Set(S.flatMap(G => Object.keys(G.catMiss)))];
    Plotly.react('a-cat', cats.map(c => ({ type: 'bar', name: c, x: S.map(G => G.name), y: S.map(G => G.catMiss[c] || 0), hovertemplate: `${c}: %{y:.2f}<extra></extra>` })),
      { ...PLOT, barmode: 'stack', yaxis: { title: 'weight missed per run', gridcolor: '#1e293b' }, legend: { orientation: 'h', y: -0.25 } }, CFG);

    const opts = S.map(G => `<option>${UI.esc(G.name)}</option>`).join('');
    const ha = document.getElementById('h-a'), hb = document.getElementById('h-b'), pa = ha.value, pb = hb.value;
    ha.innerHTML = opts; hb.innerHTML = opts;
    if (S.find(G => G.name === pa)) ha.value = pa; if (S.find(G => G.name === pb)) hb.value = pb; else if (S[1]) hb.value = S[1].name;
    window.__analysisStats = S;
    headToHead(); missedFacts(S);
  }

  // Paired sign-flip permutation test on per-repo mean recall differences.
  function permutationP(d) {
    const n = d.length; if (!n) return null;
    const obs = Math.abs(d.reduce((a, b) => a + b, 0));
    let hits = 0, total = 0;
    if (n <= 16) { for (let m = 0; m < 1 << n; m++) { let s = 0; for (let i = 0; i < n; i++) s += (m >> i) & 1 ? -d[i] : d[i]; if (Math.abs(s) >= obs - 1e-12) hits++; total++; } }
    else { for (let k = 0; k < 20000; k++) { let s = 0; for (let i = 0; i < n; i++) s += Math.random() < 0.5 ? -d[i] : d[i]; if (Math.abs(s) >= obs - 1e-12) hits++; total++; } }
    return hits / total;
  }
  function headToHead() {
    const S = window.__analysisStats || [], A = S.find(G => G.name === document.getElementById('h-a').value), B = S.find(G => G.name === document.getElementById('h-b').value);
    const sum = document.getElementById('h-sum');
    if (!A || !B || A === B) { sum.textContent = 'Pick two different entries.'; Plotly.purge('h-plot'); return; }
    const common = [...A.repoMeans.keys()].filter(r => B.repoMeans.has(r)).sort();
    const d = common.map(r => A.repoMeans.get(r) - B.repoMeans.get(r));
    const wins = d.filter(x => x > 0.005).length, losses = d.filter(x => x < -0.005).length, p = permutationP(d);
    sum.innerHTML = common.length ? `<b>${UI.esc(A.name)}</b> ${mean(d) >= 0 ? 'leads' : 'trails'} by <b>${Math.abs(Math.round(mean(d) * 1000) / 10)} points</b> on ${common.length} shared repos: ${wins} wins, ${common.length - wins - losses} ties, ${losses} losses. Paired permutation p = <b>${p.toFixed(3)}</b>${p < 0.05 ? ' (significant)' : ' (not significant)'}.`
      : 'No repos in common yet.';
    Plotly.react('h-plot', [{ type: 'bar', x: common, y: d, marker: { color: d.map(x => x >= 0 ? '#22c55e' : '#ef4444') }, hovertemplate: '%{x}: %{y:+.0%}<extra></extra>' }],
      { ...PLOT, yaxis: { tickformat: '+.0%', gridcolor: '#1e293b', zerolinecolor: '#475569' } }, CFG);
  }

  function missedFacts(S) {
    const facts = new Map();
    for (const G of S) for (const r of G.runs.filter(x => x.delivered)) {
      for (const m of r.misses || []) { const k = `${r.repo}/${m.id}`; if (!facts.has(k)) facts.set(k, { k, category: m.category, weight: m.weight, by: {} }); const f = facts.get(k); f.by[G.name] = (f.by[G.name] || 0) + 1; }
    }
    const runsPer = Object.fromEntries(S.map(G => [G.name, new Map([...G.byRepo].map(([k, v]) => [k, v.length]))]));
    const list = [...facts.values()].map(f => {
      const repo = f.k.split('/')[0]; let miss = 0, n = 0;
      for (const G of S) { const c = runsPer[G.name].get(repo) || 0; n += c; miss += f.by[G.name] || 0; }
      return { ...f, repo, rate: n ? miss / n : 0 };
    }).sort((a, b) => b.rate * b.weight - a.rate * a.weight).slice(0, 40);
    document.getElementById('a-facts').innerHTML = list.length ? `<table class="simple"><tr><th>Fact</th><th>Category</th><th>Weight</th><th>Missed overall</th>${S.map(G => `<th>${UI.esc(G.name)}</th>`).join('')}</tr>` +
      list.map(f => `<tr><td class="code">${UI.esc(f.k)}</td><td>${UI.esc(f.category)}</td><td>${f.weight}</td><td>${UI.pct(f.rate)}</td>${S.map(G => { const n = runsPer[G.name].get(f.repo) || 0; return `<td>${n ? `${f.by[G.name] || 0}/${n}` : '<span class="text-slate-600">-</span>'}</td>`; }).join('')}</tr>`).join('') + '</table>'
      : '<div class="text-slate-400 text-sm">Nothing missed.</div>';
  }

  const openResult = id => ResultView.open(id);

  window.QueueView = window.QueueView || { openJob: () => { } };
  return { mount, unmount() { if (runsTable) runsTable.destroy(); runsTable = null; } };
})());
