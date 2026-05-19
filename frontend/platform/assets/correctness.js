/* =====================================================================
   IICPC PLATFORM · CORRECTNESS VALIDATION SUITE
   ---------------------------------------------------------------------
   These are the tests a quant judge will write to break a contestant's
   matching engine. Every test:
     • Has a deterministic input sequence
     • Has an expected outcome
     • Tags the failure with a category so contestants can debug

   Categories:
     • protocol      — wire-format correctness (id, status, shape)
     • priority      — price-time priority violations
     • cross         — order crossing logic (limit/market)
     • cancel        — cancel handling correctness
     • selftrade     — self-trade prevention
     • robust        — handles malformed / hostile input
     • idempotence   — same input → same output
     • determinism   — replay produces identical sequence

   Each test returns { name, category, passed, details, severity }.
   severity ∈ { critical | major | minor | nit }.

   Tests run against ANY runtime (Runtime.load → submit). Same runner
   works for JS, Python, WASM, Judge0. This is intentional — judges
   should not be able to tell which language a submission was written
   in by reading the test report.
===================================================================== */
(function () {

  // —— Single-test helper —————————————————————————————————
  function test(name, category, severity, fn) {
    return { name, category, severity, fn };
  }

  // —— Test catalog ——————————————————————————————————————
  const TESTS = [

    // ── Protocol shape ──────────────────────────────────
    test('accepts limit order', 'protocol', 'critical', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: 100, qty: 5 });
      if (r.error) return { ok: false, details: 'submit threw: ' + r.error };
      if (!r.acks?.length) return { ok: false, details: 'no acks returned' };
      const a = r.acks[0];
      if (a.id !== 1) return { ok: false, details: `ack.id=${a.id} expected 1` };
      if (!['accepted', 'partial', 'filled'].includes(a.status))
        return { ok: false, details: `unexpected status: ${a.status}` };
      return { ok: true };
    }),

    test('accepts market order', 'protocol', 'critical', async (rt) => {
      // Seed liquidity first
      await rt.submit({ id: 10, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 11, side: 'buy', type: 'market', price: 0, qty: 3 });
      if (r.error) return { ok: false, details: 'market threw: ' + r.error };
      if (!r.fills?.length) return { ok: false, details: 'market did not fill against resting liquidity' };
      const f = r.fills[0];
      if (f.price !== 100) return { ok: false, details: `fill price ${f.price} expected 100` };
      return { ok: true };
    }),

    test('returns valid json structure', 'protocol', 'major', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: 100, qty: 1 });
      if (!Array.isArray(r.acks)) return { ok: false, details: 'acks not an array' };
      if (!Array.isArray(r.fills)) return { ok: false, details: 'fills not an array' };
      return { ok: true };
    }),

    // ── Price-time priority ─────────────────────────────
    test('price priority: better price fills first', 'priority', 'critical', async (rt) => {
      // Two sells at different prices, then one buy
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 101, qty: 5 });
      await rt.submit({ id: 2, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 3, side: 'buy', type: 'market', price: 0, qty: 5 });
      if (!r.fills?.length) return { ok: false, details: 'buy did not fill' };
      const f = r.fills[0];
      if (f.price !== 100) return { ok: false, details: `filled at ${f.price}, expected 100 (better price)` };
      return { ok: true };
    }),

    test('time priority: earlier order fills first at same price', 'priority', 'critical', async (rt) => {
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 100, qty: 5 });
      await rt.submit({ id: 2, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 3, side: 'buy', type: 'market', price: 0, qty: 3 });
      if (!r.fills?.length) return { ok: false, details: 'buy did not fill' };
      const f = r.fills[0];
      // The maker side of the fill should reference id=1 (the earlier order)
      if (f.makerId != null && f.makerId !== 1) {
        return { ok: false, details: `maker=${f.makerId}, expected 1 (FIFO)` };
      }
      return { ok: true };
    }),

    // ── Cross logic ─────────────────────────────────────
    test('limit does not cross when price unfavorable', 'cross', 'critical', async (rt) => {
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 2, side: 'buy', type: 'limit', price: 99, qty: 5 });
      if (r.fills?.length) return { ok: false, details: 'buy at 99 should not cross ask at 100' };
      return { ok: true };
    }),

    test('limit crosses fully when price aggressive', 'cross', 'major', async (rt) => {
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 100, qty: 5 });
      const r = await rt.submit({ id: 2, side: 'buy', type: 'limit', price: 101, qty: 5 });
      if (!r.fills?.length) return { ok: false, details: 'aggressive buy did not cross' };
      const totalFilled = r.fills.reduce((s, f) => s + f.qty, 0);
      if (Math.abs(totalFilled - 5) > 0.001) return { ok: false, details: `filled ${totalFilled} expected 5` };
      return { ok: true };
    }),

    test('partial fill leaves residual on book', 'cross', 'major', async (rt) => {
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 100, qty: 3 });
      const r1 = await rt.submit({ id: 2, side: 'buy', type: 'limit', price: 100, qty: 5 });
      // Now best bid should be 100, sz 2
      // We test by submitting a small sell at 100 — it should fill from the resting buy
      const r2 = await rt.submit({ id: 3, side: 'sell', type: 'market', price: 0, qty: 1 });
      if (!r2.fills?.length) return { ok: false, details: 'no residual buy at 100 after partial fill' };
      return { ok: true };
    }),

    // ── Cancel handling ─────────────────────────────────
    test('cancel removes resting order', 'cancel', 'major', async (rt) => {
      await rt.submit({ id: 1, side: 'sell', type: 'limit', price: 100, qty: 5 });
      await rt.submit({ id: 2, type: 'cancel', targetId: 1, side: 'sell', price: 100, qty: 0 });
      const r = await rt.submit({ id: 3, side: 'buy', type: 'market', price: 0, qty: 5 });
      if (r.fills?.length) return { ok: false, details: 'order 1 should have been cancelled before buy' };
      return { ok: true };
    }),

    test('cancel of unknown id does not crash', 'cancel', 'major', async (rt) => {
      const r = await rt.submit({ id: 99, type: 'cancel', targetId: 9999, side: 'buy', price: 0, qty: 0 });
      if (r.error && r.error !== 'cancel-miss') return { ok: false, details: 'crashed on unknown cancel: ' + r.error };
      return { ok: true };
    }),

    // ── Robust / hostile input ──────────────────────────
    test('rejects negative quantity', 'robust', 'major', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: 100, qty: -5 });
      const a = r.acks?.[0];
      // Either explicitly rejects, or returns 0 fills + status != accepted
      if (a?.status === 'accepted' && !r.error) {
        // Check it didn't actually take the order
        const r2 = await rt.submit({ id: 2, side: 'sell', type: 'market', price: 0, qty: 5 });
        if (r2.fills?.length) return { ok: false, details: 'negative qty became a real order' };
      }
      return { ok: true };
    }),

    test('rejects zero quantity', 'robust', 'minor', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: 100, qty: 0 });
      // Should not crash, and should not put a phantom on the book
      if (r.error) return { ok: false, details: 'crashed on zero qty: ' + r.error };
      return { ok: true };
    }),

    test('handles NaN price gracefully', 'robust', 'major', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: NaN, qty: 5 });
      // Should not crash. May reject or coerce. Just no exception.
      return { ok: true };
    }),

    test('handles unknown order type', 'robust', 'minor', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'fok', price: 100, qty: 5 });
      // Should not crash. Returning an error ack is acceptable.
      return { ok: true };
    }),

    test('handles huge quantity', 'robust', 'minor', async (rt) => {
      const r = await rt.submit({ id: 1, side: 'buy', type: 'limit', price: 100, qty: 1e15 });
      if (r.error) return { ok: false, details: 'crashed on huge qty: ' + r.error };
      return { ok: true };
    }),

    // ── Idempotence / determinism ──────────────────────
    test('many sequential limits do not crash', 'idempotence', 'major', async (rt) => {
      for (let i = 0; i < 200; i++) {
        const r = await rt.submit({ id: 1000 + i, side: i % 2 ? 'buy' : 'sell', type: 'limit', price: 100 + (i % 10), qty: 1 });
        if (r.error) return { ok: false, details: `order ${i} threw: ${r.error}` };
      }
      return { ok: true };
    }),

    test('returns ack id matches order id', 'protocol', 'major', async (rt) => {
      const ids = [42, 1337, 9999999];
      for (const id of ids) {
        const r = await rt.submit({ id, side: 'buy', type: 'limit', price: 100, qty: 1 });
        if (r.acks?.[0]?.id !== id) return { ok: false, details: `id mismatch: ack=${r.acks?.[0]?.id}, order=${id}` };
      }
      return { ok: true };
    }),
  ];

  /**
   * Run the full suite against a fresh runtime instance.
   * onProgress(idx, total, test, result) gets called as tests complete.
   * Returns the summary report.
   */
  async function runSuite(loadFreshRuntime, opts = {}) {
    const onProgress = opts.onProgress || (() => {});
    const results = [];
    for (let i = 0; i < TESTS.length; i++) {
      const t = TESTS[i];
      let rt;
      try {
        rt = await loadFreshRuntime();
        const r = await t.fn(rt);
        results.push({ name: t.name, category: t.category, severity: t.severity, passed: r.ok, details: r.details || '' });
      } catch (e) {
        results.push({ name: t.name, category: t.category, severity: t.severity, passed: false, details: 'runner threw: ' + e.message });
      } finally {
        if (rt) await rt.close().catch(() => {});
      }
      onProgress(i + 1, TESTS.length, t, results[results.length - 1]);
    }
    const summary = {
      total: results.length,
      passed: results.filter(r => r.passed).length,
      critical_failures: results.filter(r => !r.passed && r.severity === 'critical').length,
      major_failures: results.filter(r => !r.passed && r.severity === 'major').length,
      minor_failures: results.filter(r => !r.passed && r.severity === 'minor').length,
      results,
      // 0-100 correctness score derived from severity-weighted failures
      score: scoreFromResults(results),
    };
    return summary;
  }

  // —— Composite correctness score ——————————————————————
  // critical = 10pt each, major = 4pt, minor = 1pt, nit = 0.5pt
  function scoreFromResults(results) {
    const weights = { critical: 10, major: 4, minor: 1, nit: 0.5 };
    let maxScore = 0, lost = 0;
    for (const r of results) {
      const w = weights[r.severity] || 1;
      maxScore += w;
      if (!r.passed) lost += w;
    }
    return Math.max(0, Math.round(100 * (maxScore - lost) / maxScore));
  }

  window.Correctness = { runSuite, TESTS };
})();
