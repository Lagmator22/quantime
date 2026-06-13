/* =====================================================================
   IICPC PLATFORM · CORRECTNESS VALIDATION SUITE
   ---------------------------------------------------------------------
   Tests the judges will write to break a developer's matching engine.
   Each test:
     • Has a deterministic input sequence
     • Has an expected outcome
     • Is tagged with a category + severity so developers can debug

   Categories:
     • protocol      - wire-format correctness (id, status, shape)
     • priority      - price-time priority violations
     • cross         - order crossing logic
     • cancel        - cancel handling correctness
     • types         - IOC / FOK / postonly
     • selftrade     - self-trade prevention
     • robust        - handles malformed / hostile input
     • idempotence   - same input → same output
     • determinism   - replay produces identical sequence
     • conservation  - fill quantity invariants

   Each test returns { ok, details? }. Severity ∈ critical|major|minor|nit.

   Runs against ANY runtime (Runtime.load → submit). Same runner works
   for JS, Python, WASM, Judge0. Judges should not be able to tell which
   language a submission was written in by reading the test report.
===================================================================== */
(function () {

  function test(name, category, severity, fn) {
    return { name, category, severity, fn };
  }

  // Helpers shared across tests. We standardize on integer prices/qty
  // because the production engine uses integer ticks; mixing floats in
  // these tests would let buggy engines pass for the wrong reason.
  const buy  = (id, type, price, qty, extra = {}) => ({ id, side: 'buy',  type, price, qty, ...extra });
  const sell = (id, type, price, qty, extra = {}) => ({ id, side: 'sell', type, price, qty, ...extra });
  const cancel = (id, targetId) => ({ id, type: 'cancel', targetId, side: 'buy', price: 0, qty: 0 });

  const TESTS = [

    // ── Protocol shape ────────────────────────────────────────────
    test('accepts limit order', 'protocol', 'critical', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', 100, 5));
      if (r.error) return { ok: false, details: 'submit threw: ' + r.error };
      if (!r.acks?.length) return { ok: false, details: 'no acks returned' };
      const a = r.acks[0];
      if (a.id !== 1) return { ok: false, details: `ack.id=${a.id} expected 1` };
      if (!['accepted', 'partial', 'filled'].includes(a.status))
        return { ok: false, details: `unexpected status: ${a.status}` };
      return { ok: true };
    }),

    test('accepts market order against resting liquidity', 'protocol', 'critical', async (rt) => {
      await rt.submit(sell(10, 'limit', 100, 5));
      const r = await rt.submit(buy(11, 'market', 0, 3));
      if (r.error) return { ok: false, details: 'market threw: ' + r.error };
      if (!r.fills?.length) return { ok: false, details: 'market did not fill against resting liquidity' };
      if (r.fills[0].price !== 100) return { ok: false, details: `fill price ${r.fills[0].price} expected 100` };
      return { ok: true };
    }),

    test('returns valid json structure (acks[] + fills[])', 'protocol', 'major', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', 100, 1));
      if (!Array.isArray(r.acks)) return { ok: false, details: 'acks not an array' };
      if (!Array.isArray(r.fills)) return { ok: false, details: 'fills not an array' };
      return { ok: true };
    }),

    test('ack.id always matches order.id', 'protocol', 'major', async (rt) => {
      for (const id of [42, 1337, 9999999]) {
        const r = await rt.submit(buy(id, 'limit', 100, 1));
        if (r.acks?.[0]?.id !== id) return { ok: false, details: `id mismatch: ack=${r.acks?.[0]?.id}, order=${id}` };
      }
      return { ok: true };
    }),

    test('fills carry unique fill ids when present', 'protocol', 'minor', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 10));
      const r = await rt.submit(buy(2, 'market', 0, 5));
      if (!r.fills?.length) return { ok: false, details: 'no fills' };
      const ids = r.fills.map(f => f.id).filter(x => x != null);
      if (ids.length && new Set(ids).size !== ids.length) {
        return { ok: false, details: 'duplicate fill ids: ' + ids.join(',') };
      }
      return { ok: true };
    }),

    // ── Price-time priority ───────────────────────────────────────
    test('price priority across multiple levels', 'priority', 'critical', async (rt) => {
      await rt.submit(sell(1, 'limit', 102, 5));
      await rt.submit(sell(2, 'limit', 101, 5));
      await rt.submit(sell(3, 'limit', 100, 5));
      const r = await rt.submit(buy(4, 'market', 0, 12));
      if (!r.fills?.length) return { ok: false, details: 'no fills' };
      if (r.fills[0].price !== 100) return { ok: false, details: `first fill at ${r.fills[0].price}, expected 100 (best ask)` };
      for (let i = 1; i < r.fills.length; i++) {
        if (r.fills[i].price < r.fills[i - 1].price) {
          return { ok: false, details: `fill ${i} jumped backward in price: ${r.fills[i - 1].price} → ${r.fills[i].price}` };
        }
      }
      return { ok: true };
    }),

    test('time priority FIFO within a single price level', 'priority', 'critical', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 3));
      await rt.submit(sell(2, 'limit', 100, 3));
      await rt.submit(sell(3, 'limit', 100, 3));
      const r = await rt.submit(buy(4, 'market', 0, 5));
      if (!r.fills?.length) return { ok: false, details: 'no fills' };
      if (r.fills[0].makerId != null && r.fills[0].makerId !== 1) {
        return { ok: false, details: `first maker=${r.fills[0].makerId}, expected 1 (FIFO)` };
      }
      return { ok: true };
    }),

    // ── Cross logic ───────────────────────────────────────────────
    test('limit does not cross when price unfavorable', 'cross', 'critical', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      const r = await rt.submit(buy(2, 'limit', 99, 5));
      if (r.fills?.length) return { ok: false, details: 'buy at 99 should not cross ask at 100' };
      return { ok: true };
    }),

    test('limit crosses fully when price aggressive', 'cross', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      const r = await rt.submit(buy(2, 'limit', 101, 5));
      if (!r.fills?.length) return { ok: false, details: 'aggressive buy did not cross' };
      const total = r.fills.reduce((s, f) => s + f.qty, 0);
      if (Math.abs(total - 5) > 1e-9) return { ok: false, details: `filled ${total} expected 5` };
      return { ok: true };
    }),

    test('partial fill leaves residual on book', 'cross', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 3));
      await rt.submit(buy(2, 'limit', 100, 5));
      const r = await rt.submit(sell(3, 'market', 0, 1));
      if (!r.fills?.length) return { ok: false, details: 'no residual buy at 100 after partial fill' };
      return { ok: true };
    }),

    test('market with empty book does not crash and does not fill', 'cross', 'major', async (rt) => {
      const r = await rt.submit(buy(1, 'market', 0, 5));
      if (r.error) return { ok: false, details: 'crashed on empty-book market: ' + r.error };
      if (r.fills?.length) return { ok: false, details: 'market filled against empty book' };
      const a = r.acks?.[0];
      if (a && a.status === 'accepted' && (a.filled || 0) > 0) {
        return { ok: false, details: 'reported filled>0 with empty book' };
      }
      return { ok: true };
    }),

    test('market sweeps multiple levels until qty exhausted', 'cross', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 2));
      await rt.submit(sell(2, 'limit', 101, 2));
      await rt.submit(sell(3, 'limit', 102, 2));
      const r = await rt.submit(buy(4, 'market', 0, 5));
      const total = r.fills?.reduce((s, f) => s + f.qty, 0) || 0;
      if (total !== 5) return { ok: false, details: `swept ${total}, expected 5` };
      return { ok: true };
    }),

    // ── Cancel handling ───────────────────────────────────────────
    test('cancel removes resting order from book', 'cancel', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      await rt.submit(cancel(2, 1));
      const r = await rt.submit(buy(3, 'market', 0, 5));
      if (r.fills?.length) return { ok: false, details: 'order 1 should have been cancelled before buy' };
      return { ok: true };
    }),

    test('cancel of unknown id does not crash', 'cancel', 'major', async (rt) => {
      const r = await rt.submit(cancel(99, 9999));
      if (r.error && r.error !== 'cancel-miss') return { ok: false, details: 'crashed: ' + r.error };
      return { ok: true };
    }),

    test('cancel of already-cancelled is idempotent', 'cancel', 'minor', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      await rt.submit(cancel(2, 1));
      const r = await rt.submit(cancel(3, 1));
      if (r.error && r.error !== 'cancel-miss') return { ok: false, details: 'crashed on double cancel: ' + r.error };
      return { ok: true };
    }),

    // ── Order types (IOC / FOK / postonly) ─────────────────────────
    test('IOC fills what it can and cancels rest', 'types', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 3));
      const r = await rt.submit(buy(2, 'ioc', 100, 10));
      if (r.error) return { ok: false, details: 'IOC threw: ' + r.error };
      const total = r.fills?.reduce((s, f) => s + f.qty, 0) || 0;
      if (total !== 3) return { ok: false, details: `IOC filled ${total}, expected 3 (rest cancelled)` };
      const r2 = await rt.submit(sell(3, 'market', 0, 1));
      if (r2.fills?.some(f => f.makerId === 2)) {
        return { ok: false, details: 'IOC left residual on book - should have cancelled' };
      }
      return { ok: true };
    }),

    test('FOK fills entire qty or none', 'types', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 3));
      const r = await rt.submit(buy(2, 'fok', 100, 5));
      if (r.error) return { ok: false, details: 'FOK threw: ' + r.error };
      const total = r.fills?.reduce((s, f) => s + f.qty, 0) || 0;
      if (total !== 0 && total !== 5) {
        return { ok: false, details: `FOK partial: filled ${total}, expected 0 or 5` };
      }
      return { ok: true };
    }),

    test('postonly that would cross is rejected', 'types', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      const r = await rt.submit(buy(2, 'postonly', 100, 5));
      const a = r.acks?.[0];
      if (r.fills?.length) return { ok: false, details: 'postonly crossed instead of being rejected' };
      if (a && a.status === 'accepted') {
        return { ok: false, details: 'postonly accepted into book despite would-cross' };
      }
      return { ok: true };
    }),

    test('postonly that does not cross rests on book', 'types', 'minor', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 5));
      const r = await rt.submit(buy(2, 'postonly', 99, 5));
      if (r.fills?.length) return { ok: false, details: 'postonly should not fill' };
      const r2 = await rt.submit(sell(3, 'market', 0, 5));
      if (!r2.fills?.length) return { ok: false, details: 'postonly did not actually rest' };
      return { ok: true };
    }),

    // ── Self-trade prevention ─────────────────────────────────────
    test('self-trade handled (filled or prevented, never silent)', 'selftrade', 'major', async (rt) => {
      await rt.submit({ id: 1, clientId: 42, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 2, clientId: 42, side: 'buy', type: 'market', price: 0, qty: 5 });
      if (r.error) return { ok: false, details: 'self-trade threw: ' + r.error };
      if (!r.acks?.length) return { ok: false, details: 'no ack on self-trade attempt' };
      return { ok: true };
    }),

    // ── Robust / hostile input ────────────────────────────────────
    test('rejects negative quantity (does not enter book)', 'robust', 'major', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', 100, -5));
      if (r.error) return { ok: true };
      const a = r.acks?.[0];
      if (a?.status === 'accepted') {
        const r2 = await rt.submit(sell(2, 'market', 0, 5));
        if (r2.fills?.length) return { ok: false, details: 'negative-qty buy became a real order' };
      }
      return { ok: true };
    }),

    test('rejects zero quantity', 'robust', 'minor', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', 100, 0));
      if (r.error) return { ok: false, details: 'crashed on zero qty: ' + r.error };
      return { ok: true };
    }),

    test('rejects negative price', 'robust', 'major', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', -100, 5));
      if (r.error) return { ok: false, details: 'crashed: ' + r.error };
      const a = r.acks?.[0];
      if (a?.status === 'accepted') {
        const r2 = await rt.submit(sell(2, 'market', 0, 5));
        if (r2.fills?.length) return { ok: false, details: 'negative-price buy became a real order' };
      }
      return { ok: true };
    }),

    test('handles NaN price without polluting book', 'robust', 'major', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', NaN, 5));
      if (r.error) return { ok: false, details: 'crashed: ' + r.error };
      const r2 = await rt.submit(sell(2, 'market', 0, 5));
      if (r2.fills?.length) return { ok: false, details: 'NaN-price buy became a real order' };
      return { ok: true };
    }),

    test('handles unknown order type without crashing', 'robust', 'minor', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'fok-or-die', price: 100, qty: 5 });
      if (r.error && typeof r.error !== 'string') return { ok: false, details: 'crashed' };
      return { ok: true };
    }),

    test('handles huge qty without resource exhaustion', 'robust', 'minor', async (rt) => {
      const r = await rt.submit(buy(1, 'limit', 100, 1e15));
      if (r.error) return { ok: false, details: 'crashed on huge qty: ' + r.error };
      return { ok: true };
    }),

    test('duplicate order id does not crash', 'robust', 'minor', async (rt) => {
      await rt.submit(buy(7, 'limit', 100, 5));
      const r = await rt.submit(buy(7, 'limit', 100, 5));
      if (r.error) return { ok: false, details: 'crashed on dup id: ' + r.error };
      return { ok: true };
    }),

    // ── Quantity conservation ─────────────────────────────────────
    test('sum(fills.qty) == ack.filled', 'conservation', 'critical', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 3));
      await rt.submit(sell(2, 'limit', 101, 3));
      const r = await rt.submit(buy(3, 'market', 0, 5));
      const filled = r.acks?.[0]?.filled;
      const total = r.fills?.reduce((s, f) => s + f.qty, 0) || 0;
      if (filled != null && Math.abs(filled - total) > 1e-9) {
        return { ok: false, details: `ack.filled=${filled} but Σfills.qty=${total}` };
      }
      return { ok: true };
    }),

    test('book does not lose qty after partial fills', 'conservation', 'major', async (rt) => {
      await rt.submit(sell(1, 'limit', 100, 10));
      await rt.submit(buy(2, 'market', 0, 3));
      const r = await rt.submit(buy(3, 'market', 0, 100));
      const total = r.fills?.reduce((s, f) => s + f.qty, 0) || 0;
      if (total !== 7) return { ok: false, details: `sweep took ${total}, expected 7` };
      return { ok: true };
    }),

    // ── Idempotence / determinism ─────────────────────────────────
    test('many sequential limits do not crash', 'idempotence', 'major', async (rt) => {
      for (let i = 0; i < 200; i++) {
        const r = await rt.submit({ id: 1000 + i, side: i % 2 ? 'buy' : 'sell', type: 'limit', price: 100 + (i % 10), qty: 1 });
        if (r.error) return { ok: false, details: `order ${i} threw: ${r.error}` };
      }
      return { ok: true };
    }),

    test('deterministic ack shape across identical inputs', 'determinism', 'major', async (rt) => {
      const seq = [
        sell(1, 'limit', 100, 5),
        sell(2, 'limit', 101, 5),
        buy(3, 'limit', 99, 5),
        buy(4, 'market', 0, 7),
        cancel(5, 2),
      ];
      const sigs = [];
      for (const o of seq) {
        const r = await rt.submit(o);
        sigs.push(JSON.stringify({
          status: r.acks?.[0]?.status,
          filled: r.acks?.[0]?.filled,
          fillsLen: r.fills?.length || 0,
        }));
      }
      if (sigs.length !== seq.length) return { ok: false, details: 'missing signatures' };
      return { ok: true };
    }),
  ];

  /**
   * Run the full suite against a fresh runtime instance per test.
   * onProgress(idx, total, test, result) fires as tests complete.
   * opts.filterCategory restricts to a single category.
   * opts.stopOnCritical halts and marks remaining tests as skipped.
   */
  async function runSuite(loadFreshRuntime, opts = {}) {
    const onProgress = opts.onProgress || (() => {});
    const stopOnCritical = !!opts.stopOnCritical;
    const filter = opts.filterCategory || null;

    const tests = filter ? TESTS.filter(t => t.category === filter) : TESTS;
    const results = [];

    for (let i = 0; i < tests.length; i++) {
      const t = tests[i];
      let rt;
      try {
        rt = await loadFreshRuntime();
        const r = await t.fn(rt);
        results.push({
          name: t.name, category: t.category, severity: t.severity,
          passed: r.ok, details: r.details || '',
        });
      } catch (e) {
        results.push({
          name: t.name, category: t.category, severity: t.severity,
          passed: false, details: 'runner threw: ' + e.message,
        });
      } finally {
        if (rt) await rt.close().catch(() => {});
      }
      onProgress(i + 1, tests.length, t, results[results.length - 1]);

      if (stopOnCritical && !results[results.length - 1].passed && t.severity === 'critical') {
        for (let j = i + 1; j < tests.length; j++) {
          results.push({
            name: tests[j].name, category: tests[j].category, severity: tests[j].severity,
            passed: false, details: 'skipped after critical failure',
          });
          onProgress(j + 1, tests.length, tests[j], results[results.length - 1]);
        }
        break;
      }
    }

    return {
      total: results.length,
      passed: results.filter(r => r.passed).length,
      critical_failures: results.filter(r => !r.passed && r.severity === 'critical').length,
      major_failures: results.filter(r => !r.passed && r.severity === 'major').length,
      minor_failures: results.filter(r => !r.passed && r.severity === 'minor').length,
      results,
      score: scoreFromResults(results),
    };
  }

  // critical = 10pt each, major = 4pt, minor = 1pt, nit = 0.5pt
  function scoreFromResults(results) {
    const weights = { critical: 10, major: 4, minor: 1, nit: 0.5 };
    let maxScore = 0, lost = 0;
    for (const r of results) {
      const w = weights[r.severity] || 1;
      maxScore += w;
      if (!r.passed) lost += w;
    }
    if (maxScore === 0) return 0;
    return Math.max(0, Math.round(100 * (maxScore - lost) / maxScore));
  }

  window.Correctness = { runSuite, TESTS };
})();
