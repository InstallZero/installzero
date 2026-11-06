// Queue: workers, campaigns with progress, and jobs with live status and logs.
// Jobs are one page at a time; search matches repo, model, worker, error and id.
UI.register('queue', (() => {
  let table, searchTimer, loadGen = 0, jobFilter = { status: '', type: '', q: '', page: 1 };
  const PAGE = 50;

  function mount(root) {
    root.innerHTML = `
      <div class="grid grid-cols-1 xl:grid-cols-3 gap-4 mb-4">
        <div class="card"><h2>Workers</h2><div id="workers" class="text-sm"></div></div>
        <div class="card xl:col-span-2"><h2>Campaigns</h2><div id="campaigns" class="text-sm"></div></div>
      </div>
      <div class="card">
        <div class="flex flex-wrap items-center gap-2 mb-3">
          <h2 class="!mb-0 mr-auto">Jobs</h2>
          <input id="f-q" class="inp w-64" placeholder="search repo, model, worker, error">
          <select id="f-status" class="inp"><option value="">all statuses</option><option value="queued">queued</option><option value="claimed,running">running</option><option value="done">done</option><option value="failed">failed</option><option value="cancelled">cancelled</option></select>
          <select id="f-type" class="inp"><option value="">all types</option><option value="probe">probe</option><option value="build">build</option><option value="draft">draft</option><option value="measure">measure</option><option value="diagnose">diagnose</option><option value="lessons">lessons</option><option value="sft-build">sft-build</option><option value="train">train</option></select>
          <div id="f-pager" class="flex items-center gap-2"></div>
        </div>
        <div id="jobs"></div>
      </div>`;
    root.querySelector('#f-q').oninput = e => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => { jobFilter.q = e.target.value.trim(); jobFilter.page = 1; loadJobs(); }, 250);
    };
    root.querySelector('#f-status').onchange = e => { jobFilter.status = e.target.value; jobFilter.page = 1; loadJobs(); };
    root.querySelector('#f-type').onchange = e => { jobFilter.type = e.target.value; jobFilter.page = 1; loadJobs(); };

    table = new Tabulator('#jobs', {
      layout: 'fitColumns', height: '58vh', index: '_id', placeholder: 'No jobs yet. Start one from New eval, or build or draft a repo from Repos.',
      columns: [
        { title: 'Status', field: 'status', width: 100, formatter: c => UI.badge(c.getValue()) },
        { title: 'Type', field: 'type', width: 70 },
        { title: 'Repo', field: 'bench_id', width: 130, formatter: c => UI.esc(c.getValue() || c.getRow().getData().lesson_set || '') },
        { title: 'Model', field: 'model', width: 190, formatter: c => UI.esc(UI.shortModel(c.getValue())) },
        { title: 'Progress', field: 'progress', minWidth: 220, formatter: c => progressCell(c.getRow().getData()) },
        { title: 'Recall', field: 'recall', width: 75, hozAlign: 'right', formatter: c => UI.pct(c.getValue()) },
        { title: 'Ports', field: 'ports', width: 120, formatter: c => { const p = c.getValue(); return p ? UI.esc(p.join(' ')) : (c.getRow().getData().status === 'queued' ? '' : '<span class="text-amber-400">exclusive</span>'); } },
        { title: 'Worker', field: 'worker', width: 110 },
        { title: 'Time', field: 'started_at', width: 85, formatter: c => timeCell(c.getRow().getData()) },
        { title: 'Created', field: 'created_at', width: 85, formatter: c => UI.ago(c.getValue()) },
        { title: '', field: '_id', width: 150, headerSort: false, formatter: c => actions(c.getRow().getData()), cellClick: (e, c) => onAction(e, c.getRow().getData()) },
      ],
    });
    table.on('rowClick', (e, row) => { if (!e.target.closest('button')) openJob(row.getData()._id); });

    UI.every(3000, loadJobs);
    UI.every(4000, loadSide);
  }

  function progressCell(j) {
    const p = j.progress || {};
    if (j.status === 'done' && j.type === 'probe') return `<span class="text-slate-400">${UI.esc(p.msg || 'scored')}</span>`;
    if (j.status === 'failed') return `<span class="text-red-300">${UI.esc((j.error || '').slice(0, 120))}</span>`;
    if (!['running', 'claimed'].includes(j.status)) return '';
    const frac = p.turn && p.max_turns ? p.turn / p.max_turns : null;
    const head = `<div class="flex items-center gap-2"><span class="text-xs text-slate-300 w-24 truncate">${UI.esc(p.phase || j.status)}</span>`;
    if (p.pct != null) return head + `<div class="bar flex-1" title="${UI.esc(p.examples || '')} examples${p.eta_s != null ? `, about ${UI.dur(p.eta_s)} left` : ''}"><div style="width:${p.pct}%"></div></div>` +
      `<span class="text-xs w-24 text-right">${Math.round(p.pct)}%${p.eta_s != null ? ` · ${UI.dur(p.eta_s)}` : ''}</span></div>`;
    return head +
      (frac != null ? `<div class="bar flex-1"><div style="width:${Math.round(frac * 100)}%"></div></div><span class="text-xs w-12 text-right">${p.turn}/${p.max_turns}</span>` : `<span class="text-xs text-slate-400 truncate">${UI.esc(p.msg || '')}</span>`) + '</div>';
  }
  function timeCell(j) {
    if (!j.started_at) return '';
    const end = j.finished_at ? new Date(j.finished_at) : new Date();
    return UI.dur((end - new Date(j.started_at)) / 1000);
  }
  function actions(j) {
    const b = [];
    if (['queued', 'claimed', 'running'].includes(j.status)) b.push(`<button class="btn btn-xs" data-a="cancel">Cancel</button>`);
    if (['failed', 'cancelled', 'done'].includes(j.status)) b.push(`<button class="btn btn-xs" data-a="retry">Retry</button>`);
    if (!['claimed', 'running'].includes(j.status)) b.push(`<button class="btn btn-xs" data-a="delete">Delete</button>`);
    return `<div class="flex gap-1">${b.join('')}</div>`;
  }
  async function onAction(e, j) {
    const a = e.target.dataset && e.target.dataset.a;
    if (!a) return;
    await UI.act(() => UI.post('/api/job/action', { id: j._id, action: a }), `${a}: ${j.bench_id}`);
    loadJobs();
  }

  function renderPager(data) {
    const el = document.getElementById('f-pager');
    if (!el) return;
    const from = data.total ? (data.page - 1) * data.limit + 1 : 0;
    const to = Math.min(data.total, data.page * data.limit);
    el.innerHTML = `<button id="f-prev" class="btn btn-xs" ${data.page <= 1 ? 'disabled' : ''}>Prev</button>
      <span class="text-xs text-slate-400 whitespace-nowrap">${from}–${to} of ${data.total}</span>
      <button id="f-next" class="btn btn-xs" ${data.page >= data.pages ? 'disabled' : ''}>Next</button>`;
    el.querySelector('#f-prev').onclick = () => { if (jobFilter.page > 1) { jobFilter.page--; loadJobs(); } };
    el.querySelector('#f-next').onclick = () => { if (jobFilter.page < data.pages) { jobFilter.page++; loadJobs(); } };
  }

  async function loadJobs() {
    if (!table) return;
    const gen = ++loadGen;
    const q = new URLSearchParams({ limit: PAGE, page: jobFilter.page || 1 });
    if (jobFilter.status) q.set('status', jobFilter.status);
    if (jobFilter.type) q.set('type', jobFilter.type);
    if (jobFilter.q) q.set('q', jobFilter.q);
    try {
      const data = await UI.get('/api/jobs?' + q);
      if (gen !== loadGen || !table) return;
      jobFilter.page = data.page;
      await table.replaceData(data.jobs);
      renderPager(data);
    } catch (e) { }
  }

  async function loadSide() {
    try {
      const [ws, cs] = await Promise.all([UI.get('/api/workers'), UI.get('/api/campaigns')]);
      document.getElementById('workers').innerHTML = ws.length ? `<table class="simple"><tr><th>Worker</th><th>Slots</th><th>Running</th><th>Seen</th></tr>` +
        ws.map(w => `<tr><td>${w.online ? '<span class="text-emerald-400">●</span>' : '<span class="text-slate-600">●</span>'} ${UI.esc(w._id)}</td><td>${w.slots || 0} (gpu ${w.gpu_slots || 0})</td><td>${(w.running || []).length}</td><td>${UI.ago(w.last_seen)}</td></tr>`).join('') + '</table>'
        : `<div class="text-slate-400">No worker has checked in. On the Windows host run:<pre class="log mt-2">agent\\eval\\worker\\Start-EvalWorker.ps1 -Slots 3</pre></div>`;
      document.getElementById('campaigns').innerHTML = cs.length ? `<table class="simple"><tr><th>Campaign</th><th>Dataset</th><th>Models</th><th style="width:28%">Progress</th><th></th></tr>` +
        cs.slice(0, 8).map(c => {
          const n = c.counts || {}, total = Object.values(n).reduce((a, b) => a + b, 0), done = (n.done || 0) + (n.failed || 0) + (n.cancelled || 0);
          return `<tr><td>${UI.esc(c.name)}</td><td>${UI.esc(c.dataset)}</td><td class="text-xs">${c.models.map(m => UI.esc(UI.shortModel(m))).join(', ')} ×${c.runs}</td>
            <td><div class="flex items-center gap-2"><div class="bar flex-1"><div style="width:${total ? Math.round(done / total * 100) : 0}%"></div></div><span class="text-xs whitespace-nowrap">${n.done || 0}/${total}${n.failed ? ` <span class="text-red-300">${n.failed} failed</span>` : ''}${(n.running || 0) + (n.claimed || 0) ? ` <span class="text-sky-300">${(n.running || 0) + (n.claimed || 0)} running</span>` : ''}</span></div></td>
            <td class="whitespace-nowrap"><a class="btn btn-xs" href="#analysis?campaign=${encodeURIComponent(c._id)}">Analyze</a>
              <button class="btn btn-xs" onclick="QueueView.campaign('${c._id}','retry-failed')">Retry failed</button>
              <button class="btn btn-xs" onclick="QueueView.campaign('${c._id}','cancel')">Cancel</button>
              <button class="btn btn-xs" onclick="QueueView.campaign('${c._id}','delete')">Delete</button></td></tr>`;
        }).join('') + '</table>' : '<div class="text-slate-400">No campaigns yet.</div>';
    } catch (e) { }
  }

  async function openJob(id) {
    const body = UI.openDrawer(`Job ${id}`, '<div id="job-head" class="text-sm mb-3"></div><div id="job-log" class="log"></div>');
    let stick = true;
    const logEl = body.querySelector('#job-log');
    logEl.addEventListener('scroll', () => { stick = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 20; });
    const load = async () => {
      try {
        const j = await UI.get('/api/job?id=' + encodeURIComponent(id));
        body.querySelector('#job-head').innerHTML = `<div class="flex flex-wrap gap-x-6 gap-y-1">
          <span>${UI.badge(j.status)}</span><span>${UI.esc(j.type)} <b>${UI.esc(j.bench_id)}</b></span>${j.model ? `<span>${UI.esc(j.model)}</span>` : ''}
          ${j.distro ? `<span class="text-slate-400">distro ${UI.esc(j.distro)}</span>` : ''}${j.worker ? `<span class="text-slate-400">worker ${UI.esc(j.worker)}</span>` : ''}
          ${j.recall != null ? `<span>recall <b>${UI.pct(j.recall)}</b></span>` : ''}${j.result_id ? `<a class="text-sky-400" href="#analysis?result=${j.result_id}">result</a>` : ''}
          ${j.error ? `<span class="text-red-300">${UI.esc(j.error)}</span>` : ''}</div>`;
        logEl.textContent = (j.log || []).join('\n');
        if (stick) logEl.scrollTop = logEl.scrollHeight;
        return j;
      } catch (e) { }
    };
    await load();
    const t = setInterval(load, 2000);
    UI.onDrawerClose = () => clearInterval(t);
  }

  window.QueueView = {
    campaign: async (id, action) => {
      if (action === 'delete' && !confirm('Delete this campaign\'s finished and queued jobs? Results stay.')) return;
      await UI.act(() => UI.post('/api/campaign/action', { id, action }), `${action} done`); loadSide(); loadJobs();
    },
    openJob,
  };
  return { mount, unmount() { clearTimeout(searchTimer); if (table) table.destroy(); table = null; } };
})());
