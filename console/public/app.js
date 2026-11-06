// Shell: hash router, API helper, shared formatting, drawer and toasts.
window.UI = (() => {
  const views = {};
  let current = null, timers = [];

  async function api(path, opts = {}) {
    const o = { headers: { 'Content-Type': 'application/json' }, ...opts };
    if (o.body && typeof o.body !== 'string') o.body = JSON.stringify(o.body);
    const r = await fetch(path, o);
    const t = await r.text();
    let j; try { j = t ? JSON.parse(t) : null; } catch { j = t; }
    if (!r.ok) throw new Error((j && j.error) || `${r.status} ${r.statusText}`);
    return j;
  }
  const get = p => api(p), post = (p, b) => api(p, { method: 'POST', body: b }), put = (p, b) => api(p, { method: 'PUT', body: b }), del = p => api(p, { method: 'DELETE' });

  function toast(msg, err = false) {
    const d = document.createElement('div');
    d.className = 'toast' + (err ? ' err' : ''); d.textContent = msg;
    document.getElementById('toasts').appendChild(d);
    setTimeout(() => d.remove(), err ? 8000 : 3500);
  }
  async function act(fn, ok) { try { const r = await fn(); if (ok) toast(ok); return r; } catch (e) { toast(e.message, true); throw e; } }

  const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const badge = s => `<span class="badge b-${esc(s)}">${esc(s)}</span>`;
  const pct = v => v == null ? '' : `${Math.round(v * 100)}%`;
  const dur = s => { if (s == null || isNaN(s)) return ''; s = Math.round(s); const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60; return h ? `${h}h${String(m).padStart(2, '0')}m` : m ? `${m}m${String(x).padStart(2, '0')}s` : `${x}s`; };
  const ago = d => { if (!d) return ''; const s = (Date.now() - new Date(d)) / 1000; return s < 60 ? `${Math.round(s)}s ago` : s < 3600 ? `${Math.round(s / 60)}m ago` : s < 86400 ? `${Math.round(s / 3600)}h ago` : new Date(d).toLocaleDateString(); };
  const shortModel = m => String(m || '').replace(/^local:/, '').replace(/\.gguf$/, '');

  function every(ms, fn) { fn(); const t = setInterval(fn, ms); timers.push(t); return t; }
  function openDrawer(title, html) {
    document.getElementById('drawer-title').textContent = title;
    document.getElementById('drawer-body').innerHTML = html;
    document.getElementById('drawer').classList.remove('hidden');
    return document.getElementById('drawer-body');
  }
  function closeDrawer() { document.getElementById('drawer').classList.add('hidden'); if (UI.onDrawerClose) { UI.onDrawerClose(); UI.onDrawerClose = null; } }

  function register(name, view) { views[name] = view; }
  function route() {
    const [name, query] = (location.hash.slice(1) || 'queue').split('?');
    const params = Object.fromEntries(new URLSearchParams(query || ''));
    timers.forEach(clearInterval); timers = [];
    if (current && current.unmount) current.unmount();
    closeDrawer();
    document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.dataset.view === name));
    current = views[name] || views.queue;
    const root = document.getElementById('view');
    root.innerHTML = '';
    current.mount(root, params);
  }
  async function refreshSummary() {
    try {
      const s = await get('/api/summary');
      const j = s.jobs || {};
      document.getElementById('summary').innerHTML =
        `<span>workers <b class="text-slate-200">${s.workers}</b> (${s.slots} slots)</span>` +
        `<span>running <b class="text-sky-300">${(j.running || 0) + (j.claimed || 0)}</b></span>` +
        `<span>queued <b class="text-slate-200">${j.queued || 0}</b></span>` +
        `<span>snapshots <b class="text-emerald-300">${s.snapshots}</b>/${s.recipes} recipes</span>` +
        `<span>results <b class="text-slate-200">${s.results}</b></span>`;
    } catch { }
  }
  function start() {
    window.addEventListener('hashchange', route);
    document.getElementById('drawer').addEventListener('click', e => { if (e.target.id === 'drawer') closeDrawer(); });
    route(); refreshSummary(); setInterval(refreshSummary, 5000);
  }

  // Model list for pickers: hosted from the server, local from online workers
  // (also as hf:, served on Hugging Face Jobs), and any seen in results.
  async function modelOptions() {
    const m = await get('/api/models');
    return [...new Set([...m.saved, ...m.local, ...(m.hf || [])])];
  }

  return { api, get, post, put, del, toast, act, esc, badge, pct, dur, ago, shortModel, every, openDrawer, closeDrawer, register, start, modelOptions, onDrawerClose: null };
})();
