/* =====================================================================
   QuanTime · SHARED HEADER + LAYOUT INJECTION
   ---------------------------------------------------------------------
   Every platform page calls Layout.render({ active }) on load. This
   injects the sidebar + topbar so we don't duplicate markup across
   files. Edit nav links once, here.
===================================================================== */
(function () {
  const NAV = [
    { group: 'Developer', items: [
      // Submit / Run / Dashboard all route to the Console — the single
      // backend-wired page that does upload + run + live results + history.
      // (The standalone submit.html/run.html/dashboard.html are in-browser
      // reference simulations and are kept off the main flow.)
      { id: 'console',      label: 'Console',       icon: '▶', href: 'console.html' },
      { id: 'submit',       label: 'Submit Code',   icon: '↑', href: 'console.html' },
      { id: 'run',          label: 'Stress Runs',   icon: '⟁', href: 'console.html' },
      { id: 'correctness',  label: 'Correctness',   icon: '✓', href: 'correctness.html' },
      { id: 'analyze',      label: 'AI Analysis',   icon: '⊛', href: 'analyze.html' },
      { id: 'leaderboard',  label: 'Leaderboard',   icon: '#', href: 'leaderboard.html' },
    ]},
    { group: 'Reference', items: [
      { id: 'architecture', label: 'Architecture',  icon: '▤', href: 'architecture.html' },
      { id: 'docs',         label: 'API & Docs',    icon: '⌘', href: 'docs.html' },
    ]},
    { group: 'Admin', items: [
      { id: 'judge',        label: 'Judge Console', icon: '⚖', href: 'judge.html' },
    ]},
  ];

  function escape(s) { return String(s).replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c])); }

  function renderSidebar(activeId) {
    const team = Store.get('team', {});
    const subs = Store.get('submissions', []);
    const runs = Store.get('runs', []);
    const counts = {
      submit: subs.length || '',
      run: runs.length || '',
      leaderboard: '',
    };

    const groupHTML = NAV.map(g => `
      <div class="sidebar-section">
        <div class="label">${g.group}</div>
        ${g.items.map(item => `
          <a href="${item.href}" class="sidebar-link ${item.id === activeId ? 'active' : ''}">
            <span class="icon">${item.icon}</span>
            ${item.label}
            ${counts[item.id] ? `<span class="badge">${counts[item.id]}</span>` : ''}
          </a>
        `).join('')}
      </div>
    `).join('');

    return `
      <aside class="sidebar" id="iicpc-sidebar">
        <div class="sidebar-brand">
          <span class="mark"></span>
          Quan<span style="color:var(--accent);">Time</span>
          <span class="ver">v2026</span>
        </div>
        ${groupHTML}
        <div class="sidebar-foot">
          <div>team · ${escape(team.name || '-')}</div>
          <div style="margin-top:4px;">${(team.members || []).length} member(s)</div>
          <a href="../index.html" style="display:block;margin-top:8px;color:var(--text-mute);">← public site</a>
        </div>
      </aside>
    `;
  }

  function renderTopbar(crumb) {
    return `
      <div class="topbar">
        <button class="btn btn-sm menu-toggle" onclick="document.getElementById('iicpc-sidebar').classList.toggle('open')">☰</button>
        <div class="crumb">
          ${crumb.map((c, i) => i === crumb.length - 1
            ? `<span class="now">${escape(c.label)}</span>`
            : `<a href="${c.href}">${escape(c.label)}</a><span class="sep">/</span>`).join('')}
        </div>
        <div class="actions">
          <span class="pill"><span class="dot"></span>platform · live</span>
          <span class="clock" id="iicpc-clock">-</span>
        </div>
      </div>
    `;
  }

  function startClock() {
    const el = document.getElementById('iicpc-clock');
    if (!el) return;
    function tick() {
      const d = new Date();
      const pad = n => String(n).padStart(2, '0');
      el.textContent = `UTC ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`;
    }
    tick();
    setInterval(tick, 1000);
  }

  /* Public API:
       Layout.render({ active: 'dashboard', crumb: [{label,href}, ...] })
     The page must define a <div id="app-content"> that holds main
     content; this fn wraps it with sidebar+topbar.
  */
  const Layout = {
    render({ active, crumb = [] }) {
      const content = document.getElementById('app-content');
      if (!content) return console.error('[Layout] missing #app-content');
      const html = `
        <div class="app">
          ${renderSidebar(active)}
          <main class="main">
            ${renderTopbar(crumb)}
            <div class="page" id="page-inner"></div>
          </main>
        </div>
      `;
      // Move existing content into the new structure
      const wrapper = document.createElement('div');
      wrapper.innerHTML = html;
      const inner = wrapper.querySelector('#page-inner');
      while (content.firstChild) inner.appendChild(content.firstChild);
      content.replaceWith(wrapper.firstElementChild);
      startClock();
    },
  };

  // Toast helper used across pages
  function ensureToastStack() {
    let s = document.getElementById('iicpc-toasts');
    if (!s) {
      s = document.createElement('div');
      s.id = 'iicpc-toasts';
      s.className = 'toast-stack';
      document.body.appendChild(s);
    }
    return s;
  }
  window.toast = function (msg, kind = 'info', title = null) {
    const stack = ensureToastStack();
    const t = document.createElement('div');
    t.className = 'toast ' + (kind === 'error' ? 'error' : kind === 'warn' ? 'warn' : '');
    t.innerHTML = `${title ? `<div class="t">${escape(title)}</div>` : ''}<div>${escape(msg)}</div>`;
    stack.appendChild(t);
    setTimeout(() => { t.style.opacity = '0'; t.style.transition = 'opacity .3s'; }, 3000);
    setTimeout(() => t.remove(), 3400);
  };

  window.Layout = Layout;
  window.escapeHTML = escape;
})();
