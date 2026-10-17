// One scored run in the drawer: overview, diagnosis of its misses, the
// probe's transcript and the install document. Shared by Analysis and Losses.
window.ResultView = (() => {
  const CAUSE = {
    not_explored: ['never looked', 'b-failed'], out_of_budget: ['out of budget', 'b-claimed'], found_not_written: ['found, not written', 'b-running'],
    written_unmatched: ['regex too strict', 'b-recipe'], not_observable: ['not observable', 'b-recipe'], unclear: ['unclear', 'b-queued'],
    undiagnosed: ['undiagnosed', 'b-metadata'], no_transcript: ['no transcript', 'b-metadata'],
  };
  const PROBE = ['not_explored', 'out_of_budget', 'found_not_written'], BENCH = ['written_unmatched', 'not_observable'];
  const causeBadge = c => { const [l, b] = CAUSE[c] || [c, 'b-queued']; return `<span class="badge ${b}">${UI.esc(l)}</span>`; };

  async function open(id) {
    const r = await UI.get('/api/result?id=' + encodeURIComponent(id));
    if (!r) return;
    const st = (r.probe && r.probe.stats) || {};
    const body = UI.openDrawer(`${r.repo} · ${UI.shortModel(r.model)} · ${new Date(r.run_at).toLocaleString()}`, `
      <div class="grid grid-cols-5 gap-3 mb-3">
        <div class="card"><div class="kpi-l">Recall</div><div class="kpi">${r.delivered ? UI.pct(r.recall) : 'n/a'}</div></div>
        <div class="card"><div class="kpi-l">Facts</div><div class="kpi">${r.delivered ? `${r.facts_hit}/${r.facts}` : '-'}</div></div>
        <div class="card"><div class="kpi-l">Live checks</div><div class="kpi">${r.live_checks ? `${r.live_checks.passed}/${(r.live_checks.passed || 0) + (r.live_checks.failed || 0) + (r.live_checks.untested || 0)}` : '-'}</div></div>
        <div class="card"><div class="kpi-l">Turns / tools</div><div class="kpi">${st.model_turns || '-'} / ${st.tool_calls || '-'}</div></div>
        <div class="card"><div class="kpi-l">Time</div><div class="kpi">${UI.dur(st.seconds)}</div></div></div>
      <div class="text-sm mb-3">Stopped: <b>${UI.esc(r.stop_reason || '')}</b> · prompt ${UI.esc(r.prompt_version || '')}${r.tuned_on ? ' <span class="badge b-claimed">tuned on this repo</span>' : ''} · ${UI.esc(r.target_env || '')}${r.job_id ? ` · <a class="text-sky-400" href="javascript:QueueView.openJob('${r.job_id}')">job</a>` : ''}</div>
      <div class="tabs"><button data-tab="diag" class="active">Misses</button><button data-tab="tx">Transcript${r.has_transcript ? '' : ' (none)'}</button><button data-tab="md">Install document</button></div>
      <div id="rv-tab"></div>`);
    const tabs = { diag: () => diagnosis(r), tx: () => transcript(r), md: () => document_(r) };
    const show = k => { body.querySelectorAll('.tabs button').forEach(b => b.classList.toggle('active', b.dataset.tab === k)); tabs[k](); };
    body.querySelectorAll('.tabs button').forEach(b => b.onclick = () => show(b.dataset.tab));
    show('diag');
  }

  function diagnosis(r) {
    const el = document.getElementById('rv-tab'), d = r.diagnosis, byId = new Map(((d && d.misses) || []).map(m => [m.id, m]));
    const can = r.delivered && (r.misses || []).length && r.has_transcript;
    el.innerHTML = `
      <div class="flex items-center gap-3 mb-3 text-sm">
        ${d ? `<span>Diagnosed by ${UI.esc(UI.shortModel(d.model))} ${UI.ago(d.at)}</span>` : `<span class="text-slate-400">${!(r.misses || []).length ? 'Nothing missed.' : r.has_transcript ? 'Not diagnosed yet.' : r.transcript_mixed ? 'Transcript unusable: two probes wrote one file.' : 'No transcript stored, so it cannot be diagnosed. Run Push-Transcripts.ps1 on the host.'}</span>`}
        ${can ? `<button id="rv-diag" class="btn btn-xs">${d ? 'Diagnose again' : 'Diagnose'}</button>` : ''}</div>
      ${d && d.summary ? `<div class="card mb-3 text-sm">${UI.esc(d.summary)}</div>` : ''}
      ${(r.misses || []).length ? `<table class="simple mb-3"><tr><th>Fact</th><th>Category</th><th>W</th><th>Cause</th><th>Evidence</th><th>Lesson / fact fix</th></tr>
        ${r.misses.map(m => { const x = byId.get(m.id); return `<tr><td class="code">${UI.esc(m.id)}</td><td>${UI.esc(m.category)}</td><td>${m.weight}</td>
          <td>${x ? causeBadge(x.cause) : causeBadge(r.has_transcript ? 'undiagnosed' : 'no_transcript')}${x && x.seen_in_outputs ? ' <span class="text-xs text-slate-400" title="the fact regex matches raw tool output">seen</span>' : ''}</td>
          <td class="text-xs">${UI.esc(x ? x.evidence : '')}</td><td class="text-xs">${UI.esc(x ? (x.lesson || '') : '')}${x && x.fact_fix ? `<div class="code text-amber-300 mt-1">fix: ${UI.esc(x.fact_fix)}</div>` : ''}</td></tr>`; }).join('')}</table>` : ''}
      ${d && (d.habits || []).length ? `<div class="card mb-3"><h2>How it spent its turns</h2><ul class="text-sm list-disc ml-5">${d.habits.map(h => `<li>${UI.esc(h)}</li>`).join('')}</ul></div>` : ''}
      ${(r.format_problems || []).length ? `<div class="card"><h2>Format problems</h2><ul class="text-sm list-disc ml-5">${r.format_problems.map(p => `<li>${UI.esc(typeof p === 'string' ? p : JSON.stringify(p))}</li>`).join('')}</ul></div>` : ''}`;
    const b = el.querySelector('#rv-diag');
    if (b) b.onclick = async () => { const x = await UI.act(() => UI.post('/api/diagnose', { ids: [r._id], force: true })); UI.toast(x.queued ? 'Diagnosis queued' : 'Already queued'); };
  }

  async function transcript(r) {
    const el = document.getElementById('rv-tab');
    if (!r.has_transcript) { el.innerHTML = '<div class="text-slate-400 text-sm">No transcript stored with this run.</div>'; return; }
    el.innerHTML = '<div class="text-slate-400 text-sm">Loading...</div>';
    const t = await UI.get('/api/transcript?id=' + encodeURIComponent(r._id));
    const ev = (t && t.events) || [];
    el.innerHTML = `<div class="flex gap-2 mb-2"><input id="rv-q" class="inp flex-1" placeholder="filter: text in reasoning, commands or outputs"><span id="rv-n" class="text-xs text-slate-400 self-center"></span></div><div id="rv-ev"></div>`;
    let turn = 0;
    const html = ev.map(e => {
      if (e.t === 'start') return { text: `${e.system} ${e.user}`, h: `<details class="mb-2"><summary class="text-xs text-slate-400 cursor-pointer">System prompt and task (${UI.esc(e.model || '')})</summary><div class="log mt-1">${UI.esc(e.system)}\n\n--- task ---\n${UI.esc(e.user)}</div></details>` };
      if (e.t === 'assistant') {
        turn++;
        return { text: `${e.reasoning} ${e.text} ${(e.calls || []).map(c => `${c.name} ${c.args}`).join(' ')}`, h: `<div class="mt-3 text-xs"><span class="text-sky-300 font-semibold">turn ${turn}</span>
          ${e.reasoning ? `<div class="text-slate-400 italic whitespace-pre-wrap">${UI.esc(e.reasoning)}</div>` : ''}${e.text ? `<div class="whitespace-pre-wrap">${UI.esc(e.text)}</div>` : ''}
          ${(e.calls || []).map(c => `<div class="code text-emerald-300 break-all">→ ${UI.esc(c.name)} ${UI.esc(c.args)}</div>`).join('')}</div>` };
      }
      if (e.t === 'tool') return { text: `${e.name} ${e.args} ${e.out}`, h: `<details class="ml-4"><summary class="text-xs text-slate-400 cursor-pointer">← ${UI.esc(e.name)}: ${UI.esc(String(e.out || '').split('\n')[0].slice(0, 140))}</summary><div class="log">${UI.esc(e.out)}</div></details>` };
      if (e.t === 'error') return { text: e.text, h: `<div class="text-xs text-red-300 ml-4">server error: ${UI.esc(e.text)}</div>` };
      return { text: '', h: '' };
    });
    const render = () => {
      const q = el.querySelector('#rv-q').value.trim().toLowerCase();
      const shown = q ? html.filter(x => String(x.text).toLowerCase().includes(q)) : html;
      el.querySelector('#rv-ev').innerHTML = shown.map(x => x.h).join('');
      if (q) el.querySelectorAll('#rv-ev details').forEach(d => d.open = true);
      el.querySelector('#rv-n').textContent = q ? `${shown.length} of ${html.length} events` : `${turn} turns`;
    };
    el.querySelector('#rv-q').oninput = render; render();
  }

  function document_(r) {
    document.getElementById('rv-tab').innerHTML = r.install_md_text ? `<div class="log" style="max-height:none">${UI.esc(r.install_md_text)}</div>`
      : `<div class="text-slate-400 text-sm">Not stored with this run. On the host: <span class="code">${UI.esc(r.install_md || '')}</span></div>`;
  }

  return { open, causeBadge, CAUSE, PROBE, BENCH };
})();
