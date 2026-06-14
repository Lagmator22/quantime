/* =====================================================================
   QuanTime · MULTI-LANGUAGE RUNTIME
   ---------------------------------------------------------------------
   Loads developer code and exposes a uniform submit() interface
   regardless of source language. Each runtime runs in its own
   dedicated Web Worker so:
     • crashes don't kill the page
     • the main thread stays responsive
     • timeouts are enforceable (terminate the worker if it hangs)
     • memory is partly isolated (one worker per submission)

   Supported runtimes:
     • js      - JavaScript module with exports.submit (in-worker eval)
     • py      - Python via Pyodide (CPython compiled to WASM, real interp)
     • wasm    - A precompiled .wasm module exporting `submit_bytes`
     • judge0  - Remote HTTP execution via Judge0 CE API (any language)

   The contract - every runtime must satisfy:
     submit(order) → Promise<{ acks, fills, error? }>
     close()       → terminate the worker / release resources

   API on the main thread:
     const rt = await Runtime.load(submission, opts);
     const res = await rt.submit(order, { timeoutMs });
     await rt.close();

   `submission` is the row from Store.submissions; we read .lang and
   .source and pick the worker accordingly.

   Edge cases this layer handles:
     • Submission throws on any call → wrapped in { error: '...' }
     • Submission hangs → AbortController + worker.terminate()
     • Submission returns malformed output → coerced to empty arrays
     • Worker initial-load fails → returns null + toast
     • SharedArrayBuffer unavailable → message-passing fallback (default)
===================================================================== */
(function () {
  const RUNTIMES = {
    js:     'runtime-js.js',
    py:     'runtime-py.js',
    wasm:   'runtime-wasm.js',
    judge0: 'runtime-judge0.js',  // not a worker, but matches API
    cpp:    'runtime-js.js',      // fallback to JS reference (no WASM uploaded)
    rust:   'runtime-js.js',
    go:     'runtime-js.js',
  };

  // -- Default timeouts per language (Pyodide cold-start is slow) --
  const DEFAULT_TIMEOUTS = {
    js: 1000,        // 1s
    py: 5000,        // 5s (Python is slower)
    wasm: 1000,
    judge0: 30000,   // remote API, slow
  };

  // -- Track all active runtimes for global cleanup --------
  const activeRuntimes = new Set();

  /**
   * Load a submission's runtime. Returns a runtime handle or null
   * if loading fails. Loading is async (Pyodide takes ~5s to bootstrap).
   */
  async function load(submission, opts = {}) {
    const lang = submission.lang || 'js';
    const workerScript = 'assets/' + (RUNTIMES[lang] || RUNTIMES.js);
    const w = new Worker(workerScript);

    return new Promise((resolve, reject) => {
      let resolved = false;
      let pendingReqs = new Map();   // reqId -> {resolve, reject, timeout}
      let nextReqId = 1;
      let closed = false;

      const onMsg = (e) => {
        const m = e.data;
        if (m.type === 'ready') {
          if (resolved) return;
          resolved = true;
          const rt = {
            lang,
            submissionId: submission.id,
            // -- main API: send a single order, get a response -----
            async submit(order, callOpts = {}) {
              if (closed) return { acks: [], fills: [], error: 'runtime closed' };
              const reqId = nextReqId++;
              const timeoutMs = callOpts.timeoutMs ?? DEFAULT_TIMEOUTS[lang] ?? 1000;
              return new Promise((res) => {
                const to = setTimeout(() => {
                  pendingReqs.delete(reqId);
                  // Worker has hung - terminate and mark closed.
                  // The next call will surface "runtime closed".
                  try { w.terminate(); } catch {}
                  closed = true;
                  res({ acks: [{ id: order.id, status: 'timeout' }], fills: [], error: 'timeout >' + timeoutMs + 'ms' });
                }, timeoutMs);
                pendingReqs.set(reqId, { resolve: res, timeout: to });
                w.postMessage({ type: 'submit', reqId, order });
              });
            },
            async snapshot() {
              if (closed) return null;
              const reqId = nextReqId++;
              return new Promise((res) => {
                const to = setTimeout(() => { pendingReqs.delete(reqId); res(null); }, 500);
                pendingReqs.set(reqId, { resolve: res, timeout: to });
                w.postMessage({ type: 'snapshot', reqId });
              });
            },
            async close() {
              closed = true;
              for (const p of pendingReqs.values()) clearTimeout(p.timeout);
              pendingReqs.clear();
              try { w.terminate(); } catch {}
              activeRuntimes.delete(rt);
            },
            isClosed() { return closed; },
          };
          activeRuntimes.add(rt);
          resolve(rt);
        } else if (m.type === 'load-error') {
          if (resolved) return;
          resolved = true;
          try { w.terminate(); } catch {}
          reject(new Error(m.message || 'runtime load failed'));
        } else if (m.type === 'response') {
          const p = pendingReqs.get(m.reqId);
          if (p) {
            clearTimeout(p.timeout);
            pendingReqs.delete(m.reqId);
            // Defensive: ensure shape
            const out = m.result || {};
            p.resolve({
              acks: Array.isArray(out.acks) ? out.acks : [],
              fills: Array.isArray(out.fills) ? out.fills : [],
              error: out.error || null,
            });
          }
        } else if (m.type === 'log') {
          // Forward through callback if registered
          if (opts.onLog) opts.onLog(m.level, m.msg);
        }
      };

      w.addEventListener('message', onMsg);
      w.addEventListener('error', (err) => {
        if (resolved) return;
        resolved = true;
        try { w.terminate(); } catch {}
        reject(err);
      });

      // -- Init message: hand the worker its source ---------
      w.postMessage({
        type: 'init',
        lang,
        source: submission.source || null,
        wasm: submission.wasm || null,    // ArrayBuffer if .wasm upload
        endpoint: submission.endpoint || null,  // for Judge0
        config: opts.config || {},
      });

      // Top-level load timeout
      const loadTimeout = setTimeout(() => {
        if (resolved) return;
        resolved = true;
        try { w.terminate(); } catch {}
        reject(new Error('runtime took >' + (opts.loadTimeoutMs || 30000) + 'ms to initialize'));
      }, opts.loadTimeoutMs || 30000);
      // Clear the timeout once ready
      w.addEventListener('message', function once(e) {
        if (e.data.type === 'ready' || e.data.type === 'load-error') {
          clearTimeout(loadTimeout);
          w.removeEventListener('message', once);
        }
      });
    });
  }

  /**
   * Close every active runtime - useful when the user navigates away.
   */
  async function closeAll() {
    for (const rt of [...activeRuntimes]) {
      try { await rt.close(); } catch {}
    }
  }
  window.addEventListener('beforeunload', closeAll);

  window.Runtime = { load, closeAll, RUNTIMES };
})();
