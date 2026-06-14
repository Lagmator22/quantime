/* =====================================================================
   QuanTime · API BRIDGE
   ---------------------------------------------------------------------
   The frontend was built first as a self-contained prototype that
   persists to localStorage and runs the matching engine in a Web
   Worker. This file detects the real backend and, when available,
   transparently routes Store reads/writes through the gateway.

   Behavior:
     • On boot, GET /api/health with a 750ms timeout.
     • If 200 OK → API.online = true; pages should call API.* instead
       of Store.* for shared state (submissions, runs, leaderboard).
     • If not OK → API.online = false; pages fall back to Store.* and
       the in-browser engine. Identical UX, no errors.

   No build step. Drop this file in <script>, after store.js, before
   page-specific code, and check API.online before any network call.

   Primary consumer: console.html (the fully backend-wired upload →
   run → live-results → history page). leaderboard.html and analyze.html
   also call the gateway directly. submit.html / run.html / dashboard.html
   are in-browser prototypes kept off the nav; correctness.html runs its
   own in-browser suite and is intentionally not wired.
===================================================================== */
(function () {
  const BASE = window.IICPC_API_BASE || '';   // same-origin by default
  const HEALTH_TIMEOUT_MS = 750;

  const API = {
    online: false,
    base: BASE,

    // ── Probe ────────────────────────────────────────────────────
    async detect() {
      const ctrl = new AbortController();
      const to = setTimeout(() => ctrl.abort(), HEALTH_TIMEOUT_MS);
      try {
        const r = await fetch(BASE + '/api/health', { signal: ctrl.signal });
        API.online = r.ok;
      } catch {
        API.online = false;
      } finally {
        clearTimeout(to);
      }
      return API.online;
    },

    // ── Submissions ─────────────────────────────────────────────
    async createSubmission({ teamId, name, lang, file }) {
      const fd = new FormData();
      fd.append('teamId', teamId);
      fd.append('name', name);
      fd.append('lang', lang);
      fd.append('source', file);
      const r = await fetch(BASE + '/api/submissions', { method: 'POST', body: fd });
      if (!r.ok) throw new Error('upload failed: ' + r.status);
      return r.json();
    },

    async getSubmission(id) {
      const r = await fetch(BASE + '/api/submissions/' + encodeURIComponent(id));
      if (!r.ok) return null;
      return r.json();
    },

    // ── Runs ────────────────────────────────────────────────────
    async startRun({ submissionId, profile = 'sustained', seed = 42, durationSec = 30, botsPerFleet = 50, targetRatePerBot = 0 }) {
      const r = await fetch(BASE + '/api/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ submissionId, profile, seed, durationSec, botsPerFleet, targetRatePerBot }),
      });
      if (!r.ok) throw new Error('run start failed: ' + r.status);
      return r.json();
    },

    async cancelRun(id) {
      const r = await fetch(BASE + '/api/runs/' + encodeURIComponent(id) + '/cancel', { method: 'POST' });
      return r.ok;
    },

    // getRun(id) → the authoritative run record (status, score). Used as a
    // fallback when the live WebSocket misses the 'final' event.
    async getRun(id) {
      const r = await fetch(BASE + '/api/runs/' + encodeURIComponent(id));
      if (!r.ok) return null;
      return r.json();
    },

    // streamRun(id, onMsg, onClose) → returns { close() }
    // Single-shot WebSocket subscription. Auto-reconnects if the
    // server drops us (up to 3 attempts with 1s backoff).
    streamRun(id, onMsg, onClose) {
      let ws, closed = false, attempts = 0;
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const url = `${proto}://${location.host}${BASE}/ws/runs/${encodeURIComponent(id)}`;
      const open = () => {
        ws = new WebSocket(url);
        ws.onmessage = e => { try { onMsg(JSON.parse(e.data)); } catch {} };
        ws.onclose = () => {
          if (closed) return;
          if (++attempts > 3) { onClose && onClose(); return; }
          setTimeout(open, 1000);
        };
      };
      open();
      return { close() { closed = true; try { ws.close(); } catch {} } };
    },

    // ── Leaderboard ─────────────────────────────────────────────
    async leaderboard() {
      const r = await fetch(BASE + '/api/leaderboard');
      if (!r.ok) throw new Error('leaderboard fetch failed');
      return r.json();
    },
  };

  // Probe immediately; pages can `await window.API.ready` to wait.
  API.ready = API.detect().then(() => {
    if (API.online) {
      console.info('[api] backend detected - using gateway at', BASE || location.origin);
    } else {
      console.info('[api] no backend - running in standalone prototype mode');
    }
  });

  window.API = API;
})();
