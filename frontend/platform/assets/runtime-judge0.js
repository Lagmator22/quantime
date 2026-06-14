/* =====================================================================
   QuanTime · JUDGE0 REMOTE RUNTIME WORKER
   ---------------------------------------------------------------------
   Adapter for the Judge0 CE multi-language execution API. Lets a
   developer submit code in 60+ languages and have it run on a real
   server-side sandbox. We POST each order as stdin, parse stdout.

   Default endpoint: https://ce.judge0.com  (public free tier, rate-
   limited). For production use, point at a self-hosted Judge0
   instance via submission.endpoint.

   This is slow (~500ms per request to the public API) and is meant
   to demonstrate the architecture works with remote sandboxes - NOT
   to clock peak TPS. Bot fleet will throttle accordingly.

   Developer must write code that reads one JSON order from stdin
   and writes one JSON response to stdout, then exits. We compile
   once on first request and then call /submissions repeatedly.

   For peak performance, switch the architecture to a persistent
   HTTP server inside the sandbox (Docker exec_run + keep-alive).
===================================================================== */

let endpoint = 'https://ce.judge0.com';
let languageId = 71;       // Python by default
let cachedSource = '';

function log(level, msg) { self.postMessage({ type: 'log', level, msg }); }

// Judge0 language IDs - see https://ce.judge0.com/languages
const LANG_IDS = {
  cpp: 54,     // GCC 9.2
  rust: 73,
  go: 60,
  py: 71,      // Python 3.8
  js: 63,      // Node 12
};

self.addEventListener('message', async (e) => {
  const m = e.data;

  if (m.type === 'init') {
    endpoint = m.endpoint || endpoint;
    languageId = LANG_IDS[m.config?.lang] || LANG_IDS.py;
    cachedSource = m.source || '';
    log('info', `judge0 runtime ready · endpoint=${endpoint} · language_id=${languageId}`);
    log('warn', 'remote runtime is rate-limited - TPS will be capped');
    self.postMessage({ type: 'ready' });
    return;
  }

  if (m.type === 'submit') {
    const t0 = performance.now();
    let result;
    try {
      const stdin = JSON.stringify(m.order) + '\n';
      const res = await fetch(endpoint + '/submissions?base64_encoded=false&wait=true', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          language_id: languageId,
          source_code: cachedSource,
          stdin,
          cpu_time_limit: 2,
          memory_limit: 256000,
        }),
      });
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const data = await res.json();
      const stdout = (data.stdout || '').trim();
      if (data.status?.id > 3) {
        // Statuses 4+ are errors (compilation, runtime, TLE, etc.)
        throw new Error(data.status?.description || 'judge0 error');
      }
      const parsed = JSON.parse(stdout);
      result = {
        acks: Array.isArray(parsed.acks) ? parsed.acks : [],
        fills: Array.isArray(parsed.fills) ? parsed.fills : [],
      };
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
    self.postMessage({ type: 'response', reqId: m.reqId, result: null });
  }
});
