// New eval: dataset x models x runs -> a campaign of queued probe jobs.
UI.register('launch', (() => {
  async function mount(root, params) {
    root.innerHTML = `<div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <div class="card lg:col-span-2">
        <h2>New eval campaign</h2>
        <div class="grid grid-cols-2 gap-4">
          <div><label class="lbl">Dataset</label><select id="ds" class="inp w-full"></select><div id="ds-info" class="text-xs text-slate-400 mt-1"></div></div>
          <div><label class="lbl">Name (optional)</label><input id="name" class="inp w-full" placeholder="defaults to dataset + time"></div>
          <div class="col-span-2"><label class="lbl">Models</label><div id="models" class="grid grid-cols-2 md:grid-cols-3 gap-1 text-sm"></div>
            <div class="flex gap-2 mt-2"><input id="custom" class="inp flex-1" placeholder="another model: provider:model-id or a .gguf file name"><button id="add-model" class="btn">Add</button></div></div>
          <div><label class="lbl">Runs per repo and model</label><input id="runs" type="number" min="1" value="1" class="inp w-full"></div>
          <div><label class="lbl">Max turns</label><input id="turns" type="number" min="5" value="40" class="inp w-full"></div>
          <div><label class="lbl">Priority (higher runs first)</label><input id="prio" type="number" value="0" class="inp w-full"></div>
          <div><label class="lbl">Probe guidance (an active lesson set from the Losses tab)</label><select id="guid" class="inp w-full"><option value="">none (base prompt)</option></select>
            <div id="guid-info" class="text-xs mt-1"></div></div>
          <div class="col-span-2"><label class="flex items-center gap-2 text-sm"><input id="diag" type="checkbox" checked> Diagnose misses after each run (one hosted-model call per run with misses; feeds the Losses tab)</label></div>
        </div>
        <div class="flex items-center gap-3 mt-4"><button id="go" class="btn btn-primary">Queue campaign</button><span id="est" class="text-sm text-slate-400"></span></div>
      </div>
      <div class="card text-sm text-slate-300 leading-relaxed">
        <h2>How it runs</h2>
        <p>Each job restores the repo's snapshot into a throwaway WSL distro, runs the probe with the chosen model, scores it against the repo's facts and deletes the distro.</p>
        <p class="mt-2">Workers on the Windows host take jobs in priority order. WSL distros share one network, so jobs run in parallel unless their snapshots listen on the same ports, and only one Docker-based install (Harbor, Paperless, Authentik, Mattermost, ...) runs at a time. A snapshot whose ports are not yet known is measured first. Local models share the GPU, one run at a time.</p>
        <p class="mt-2">Only repos with a ready snapshot and facts are queued; the rest are listed as skipped.</p>
      </div></div>`;

    const [dsList, models, sets, repoList] = await Promise.all([UI.get('/api/datasets'), UI.modelOptions(), UI.get('/api/lessons'), UI.get('/api/repos')]);
    const ds = root.querySelector('#ds');
    ds.innerHTML = dsList.map(d => `<option value="${UI.esc(d._id)}">${UI.esc(d.name)}${d.role ? ` [${d.role === 'heldout' ? 'held-out' : 'tuning'}]` : ''} (${d.runnable}/${d.repos.length} runnable)</option>`).join('') || '<option value="">no datasets yet</option>';
    if (params.dataset) ds.value = params.dataset;
    const guid = root.querySelector('#guid');
    guid.insertAdjacentHTML('beforeend', sets.filter(L => L.status === 'active').map(L => `<option value="${UI.esc(L._id)}">${UI.esc(L._id)}: ${UI.esc(L.name)}</option>`).join(''));
    if (params.guidance) guid.value = params.guidance;
    const benchOf = new Map(repoList.map(r => [r.id, r.bench_id]));
    const modelsEl = root.querySelector('#models');
    const boxes = () => [...modelsEl.querySelectorAll('input[type=checkbox]')];
    const listed = (m, except) => boxes().some(i => i !== except && i.value === m);
    // Without a provider prefix the worker runs the name as a local model file.
    const checkName = m => { if (!/^[\w.-]+:\S+$/.test(m) && !/\.gguf$/i.test(m)) UI.toast(`"${m}" has no provider prefix, so it would run as a local model file. Hosted models are provider:model-id, e.g. openai:${m}.`, true); };
    // Models added here can be renamed (✎) or removed (×); the listed ones come from the server.
    const addModel = (m, checked, custom) => {
      if (listed(m)) return false;
      modelsEl.insertAdjacentHTML('beforeend', `<label class="flex items-center gap-2 min-w-0"><input type="checkbox" value="${UI.esc(m)}" ${checked ? 'checked' : ''}> <span class="truncate" title="${UI.esc(m)}">${UI.esc(m)}</span>${custom
        ? '<button type="button" class="m-edit text-slate-400 hover:text-white" title="Rename">✎</button><button type="button" class="m-del text-slate-400 hover:text-red-300" title="Remove">×</button>' : ''}</label>`);
      return true;
    };
    models.forEach((m, i) => addModel(m, i === 0 && !params.models));
    if (params.models) for (const m of params.models.split(',').filter(Boolean)) { if (!addModel(m, true, true)) boxes().find(b => b.value === m).checked = true; }
    const custom = root.querySelector('#custom');
    const addCustom = () => { const v = custom.value.trim(); if (!v) return; if (addModel(v, true, true)) checkName(v); else UI.toast(`${v} is already in the list`); custom.value = ''; estimate(); };
    root.querySelector('#add-model').onclick = addCustom;
    custom.addEventListener('keydown', e => { if (e.key === 'Enter') addCustom(); });
    modelsEl.addEventListener('click', e => {
      const btn = e.target.closest('.m-edit, .m-del');
      if (!btn) return;
      e.preventDefault();
      const row = btn.closest('label'), box = row.querySelector('input[type=checkbox]');
      if (btn.classList.contains('m-del')) { row.remove(); estimate(); return; }
      const span = row.querySelector('span');
      if (!span) return;
      const ed = document.createElement('input');
      ed.className = 'inp flex-1 min-w-0 py-0'; ed.value = box.value;
      span.replaceWith(ed); ed.focus(); ed.select();
      let done = false;
      const finish = save => {
        if (done) return; done = true;
        const v = ed.value.trim();
        if (save && v && v !== box.value) {
          if (listed(v, box)) UI.toast(`${v} is already in the list`);
          else { box.value = v; checkName(v); }
        }
        span.textContent = box.value; span.title = box.value;
        ed.replaceWith(span); estimate();
      };
      ed.addEventListener('keydown', k => { if (k.key === 'Enter') { k.preventDefault(); finish(true); } else if (k.key === 'Escape') finish(false); });
      ed.addEventListener('blur', () => finish(true));
    });

    const estimate = () => {
      const d = dsList.find(x => x._id === ds.value);
      const n = [...modelsEl.querySelectorAll('input:checked')].length, runs = Number(root.querySelector('#runs').value || 1);
      root.querySelector('#ds-info').textContent = d ? `${d.description || ''} ${d.frozen ? '(frozen)' : ''}` : '';
      root.querySelector('#est').textContent = d ? `${d.runnable * n * runs} jobs (${d.runnable} runnable repos × ${n} models × ${runs} runs)` : '';
      const L = sets.find(x => x._id === guid.value), info = root.querySelector('#guid-info');
      if (!L || !d) { info.innerHTML = ''; return; }
      const tuned = d.repos.filter(id => (L.source.repos || []).includes(benchOf.get(id))).length;
      info.innerHTML = tuned ? `<span class="text-amber-300">${tuned} of ${d.repos.length} repos were used to draft ${UI.esc(L._id)}; their runs are tagged and excluded in Analysis by default. Use a held-out dataset to measure the lessons.</span>`
        : `<span class="text-emerald-300">None of these repos were used to draft ${UI.esc(L._id)}: a clean held-out measurement.</span>`;
    };
    root.addEventListener('change', estimate); root.addEventListener('input', estimate); estimate();

    root.querySelector('#go').onclick = async () => {
      const body = { dataset: ds.value, name: root.querySelector('#name').value.trim(), models: [...modelsEl.querySelectorAll('input:checked')].map(i => i.value),
        runs: Number(root.querySelector('#runs').value), max_turns: Number(root.querySelector('#turns').value), priority: Number(root.querySelector('#prio').value),
        guidance: guid.value || null, diagnose: root.querySelector('#diag').checked };
      const r = await UI.act(() => UI.post('/api/campaigns', body));
      UI.toast(`Queued ${r.jobs} jobs${r.skipped.length ? `; skipped ${r.skipped.length} repos without a snapshot or facts` : ''}${r.tuned_on ? `; ${r.tuned_on} repos were used to draft the guidance` : ''}`);
      location.hash = '#queue';
    };
  }
  return { mount };
})());
