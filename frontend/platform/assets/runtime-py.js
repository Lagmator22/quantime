/* =====================================================================
   IICPC PLATFORM · PYTHON RUNTIME WORKER (Pyodide)
   ---------------------------------------------------------------------
   Real CPython compiled to WASM via Pyodide. Cold-start is ~5s for
   the first load, then warm. Developer writes:

     def submit(order):
         # order is a dict
         return {"acks": [{"id": order["id"], "status": "accepted"}],
                 "fills": []}

     # optional:
     def snapshot():
         return {"bids": [...], "asks": [...]}

   We translate Python dicts <-> JS objects at the boundary.
===================================================================== */
const PYODIDE_VERSION = '0.26.2';
const PYODIDE_URL = `https://cdn.jsdelivr.net/pyodide/v${PYODIDE_VERSION}/full/pyodide.js`;

let pyodide = null;
let userSubmit = null;
let userSnapshot = null;

function log(level, msg) { self.postMessage({ type: 'log', level, msg }); }

self.addEventListener('message', async (e) => {
  const m = e.data;

  if (m.type === 'init') {
    try {
      log('info', `loading pyodide ${PYODIDE_VERSION} (cold-start ~3-5s)…`);
      importScripts(PYODIDE_URL);
      pyodide = await loadPyodide({
        indexURL: `https://cdn.jsdelivr.net/pyodide/v${PYODIDE_VERSION}/full/`,
      });
      log('ok', 'pyodide loaded · cpython ' + pyodide.version);

      if (m.source && m.source.trim()) {
        // Execute the developer module
        try {
          pyodide.runPython(m.source);
        } catch (err) {
          log('err', 'python module init failed: ' + err.message);
          self.postMessage({ type: 'load-error', message: err.message });
          return;
        }

        // Bind exports
        const globals = pyodide.globals;
        if (typeof globals.get('submit') === 'function') {
          userSubmit = globals.get('submit');
          log('ok', 'submit() detected');
        } else {
          log('warn', 'no submit() defined in source');
        }
        if (typeof globals.get('snapshot') === 'function') {
          userSnapshot = globals.get('snapshot');
        }
      } else {
        log('info', 'no source provided');
      }

      self.postMessage({ type: 'ready' });
    } catch (err) {
      log('err', 'pyodide bootstrap failed: ' + err.message);
      self.postMessage({ type: 'load-error', message: err.message });
    }
    return;
  }

  if (m.type === 'submit') {
    const t0 = performance.now();
    let result;
    try {
      if (!userSubmit) throw new Error('no submit() defined');
      // Convert JS order → Python dict (PyProxy handles this)
      const pyOrder = pyodide.toPy(m.order);
      const pyResult = userSubmit(pyOrder);
      pyOrder.destroy();
      // Convert Python result → JS object
      const obj = pyResult.toJs({ dict_converter: Object.fromEntries });
      pyResult.destroy();
      result = {
        acks: Array.isArray(obj.acks) ? obj.acks : [],
        fills: Array.isArray(obj.fills) ? obj.fills : [],
      };
      result.acks = result.acks.filter(a => a && a.id != null);
      result.fills = result.fills.filter(f => f && Number.isFinite(f.price) && Number.isFinite(f.qty));
    } catch (e) {
      result = {
        acks: [{ id: m.order.id, status: 'error', message: e.message }],
        fills: [],
        error: e.message,
      };
    }
    result.latencyNs = (performance.now() - t0) * 1e6;
    self.postMessage({ type: 'response', reqId: m.reqId, result });
    return;
  }

  if (m.type === 'snapshot') {
    let snap = null;
    try {
      if (userSnapshot) {
        const pyResult = userSnapshot();
        snap = pyResult.toJs({ dict_converter: Object.fromEntries });
        pyResult.destroy();
      }
    } catch (e) {}
    self.postMessage({ type: 'response', reqId: m.reqId, result: snap });
    return;
  }
});
