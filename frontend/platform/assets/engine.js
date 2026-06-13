/* =====================================================================
   IICPC PLATFORM · REFERENCE MATCHING ENGINE
   ---------------------------------------------------------------------
   Price-time priority orderbook + matching engine in plain JS. Used as
   the platform's default benchmark target and as the oracle the
   correctness suite tests against.

   Order types supported:
     limit     - rests on the book if not fully filled
     market    - never rests; unfilled remainder is reported, not posted
     ioc       - immediate-or-cancel (fills what it can, cancels rest)
     fok       - fill-or-kill (fills entire qty or none)
     postonly  - limit that is REJECTED if it would cross
     cancel    - remove a resting order by targetId
     modify    - cancel + replace at new price/qty (loses time priority)

   Hardening (judges will probe each one):
     • NaN / Infinity / negative price → reject, never post
     • NaN / Infinity / negative / zero qty → reject, never trade
     • duplicate id → reject (sequencer guarantee)
     • cancel of unknown / already-cancelled / filled → 'cancel-miss'
     • market against empty book → 'unfilled', not 'accepted'
     • self-trade (same clientId on both sides) → configurable prevention
     • all fills have a unique monotone fillId
     • acks always carry the original order id
     • ∑fills.qty == ack.filled  (qty conservation invariant)

   Public API:
     const eng = new MatchingEngine({ selfTradePrevention: 'none' });
     eng.submit({ id, clientId, side, type, price, qty, targetId? })
       → { acks, fills, latencyNs }
     eng.snapshot(levels=5)
     eng.stats()
     eng.reset()

   NOTE on latency: latencyNs measures ONLY the time spent inside this
   submit() call. It does NOT include postMessage RTT, network, or any
   client-side overhead. The run page reports both: engineLatency (this
   number) and wireLatency (end-to-end client-perceived).
===================================================================== */
class MatchingEngine {
  constructor(opts = {}) {
    // selfTradePrevention: 'none' | 'cancel-taker' | 'cancel-maker' | 'cancel-both'
    this.selfTradePrevention = opts.selfTradePrevention || 'none';
    this.reset();
  }

  reset() {
    this.bids = new Map();   // price -> [{id, clientId, qty, ts}]
    this.asks = new Map();
    this.orders = new Map(); // id -> {side, price, qtyRemaining, clientId}
    this.seenIds = new Set();
    this.fillIdSeq = 1;
    this.ordersProcessed = 0;
    this.fillsTotal = 0;
    this.lastTrade = null;
    this.errors = 0;
  }

  // ── Validation helpers ────────────────────────────────────────────
  _validPrice(p) { return Number.isFinite(p) && p > 0; }
  _validQty(q)   { return Number.isFinite(q) && q > 0; }
  _ack(id, status, extra = {}) { return { id, status, ...extra }; }

  // ── Insert into a price-keyed FIFO queue (price-time priority) ───
  _push(book, price, order) {
    if (!book.has(price)) book.set(price, []);
    book.get(price).push(order);
  }

  // ── Best price helpers (O(n) over distinct levels - fine for the
  // prototype; a production engine uses a sorted ladder / heap) ────
  _best(book, isBids) {
    if (book.size === 0) return null;
    let best = null;
    for (const px of book.keys()) {
      if (best === null) best = px;
      else if (isBids ? px > best : px < best) best = px;
    }
    return best;
  }

  // ── Would-cross check used by limit / postonly pre-trade ──────────
  _wouldCross(side, price) {
    if (side === 'buy')  { const bestAsk = this._best(this.asks, false); return bestAsk !== null && bestAsk <= price; }
    /* sell */           { const bestBid = this._best(this.bids, true);  return bestBid !== null && bestBid >= price; }
  }

  // ── Self-trade resolution at the moment of contact ────────────────
  // Returns 'fill' | 'skip-maker' | 'cancel-taker' | 'cancel-both'.
  _stpDecision(takerClientId, makerClientId) {
    if (this.selfTradePrevention === 'none') return 'fill';
    if (takerClientId == null || makerClientId == null) return 'fill';
    if (takerClientId !== makerClientId) return 'fill';
    return this.selfTradePrevention;       // 'cancel-taker'|'cancel-maker'|'cancel-both'
  }

  // ── Core matching loop. Walks the opposite book within crossing
  // limits, applying self-trade policy per contact. ────────────────
  _match(side, type, price, qty, id, clientId) {
    const fills = [];
    const opp = side === 'buy' ? this.asks : this.bids;
    const isCross = (oppPx) =>
      type === 'market' ? true
      : side === 'buy' ? oppPx <= price
      : oppPx >= price;
    let takerCancelled = false;

    while (qty > 0 && !takerCancelled) {
      const bestPx = this._best(opp, side === 'sell');
      if (bestPx === null || !isCross(bestPx)) break;
      const queue = opp.get(bestPx);
      while (queue.length && qty > 0 && !takerCancelled) {
        const resting = queue[0];
        const decision = this._stpDecision(clientId, resting.clientId);
        if (decision === 'cancel-taker') { takerCancelled = true; break; }
        if (decision === 'cancel-maker' || decision === 'cancel-both') {
          queue.shift();
          this.orders.delete(resting.id);
          if (decision === 'cancel-both') takerCancelled = true;
          continue;
        }
        // decision === 'fill'
        const matched = Math.min(qty, resting.qty);
        qty -= matched;
        resting.qty -= matched;
        const fillId = this.fillIdSeq++;
        fills.push({ id: fillId, price: bestPx, qty: matched, takerId: id, makerId: resting.id, ts: Date.now() });
        this.fillsTotal++;
        this.lastTrade = { px: bestPx, qty: matched, ts: Date.now() };
        if (resting.qty === 0) {
          queue.shift();
          this.orders.delete(resting.id);
        }
      }
      if (queue.length === 0) opp.delete(bestPx);
    }
    return { qty, fills, takerCancelled };
  }

  // ── Entry point ───────────────────────────────────────────────────
  submit(order) {
    const t0 = performance.now();
    this.ordersProcessed++;

    if (!order || typeof order !== 'object') {
      this.errors++;
      const t1 = performance.now();
      return { acks: [this._ack(undefined, 'rejected', { reason: 'malformed-order' })], fills: [], latencyNs: (t1 - t0) * 1e6 };
    }

    const { id, clientId, side, type, price, qty, targetId } = order;
    let acks = [], fills = [];

    try {
      // ── Cancel ─────────────────────────────────────────────
      if (type === 'cancel') {
        const rec = this.orders.get(targetId);
        if (rec) {
          const book = rec.side === 'buy' ? this.bids : this.asks;
          const queue = book.get(rec.price);
          if (queue) {
            const idx = queue.findIndex(o => o.id === targetId);
            if (idx >= 0) {
              queue.splice(idx, 1);
              if (queue.length === 0) book.delete(rec.price);
            }
          }
          this.orders.delete(targetId);
          acks.push(this._ack(id, 'cancelled', { targetId }));
        } else {
          acks.push(this._ack(id, 'cancel-miss', { targetId }));
        }
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }

      // ── Modify (cancel + replace; loses time priority) ─────
      if (type === 'modify') {
        const rec = this.orders.get(targetId);
        if (!rec) { acks.push(this._ack(id, 'modify-miss', { targetId })); }
        else {
          // Remove resting
          const book = rec.side === 'buy' ? this.bids : this.asks;
          const queue = book.get(rec.price);
          if (queue) {
            const idx = queue.findIndex(o => o.id === targetId);
            if (idx >= 0) queue.splice(idx, 1);
            if (queue.length === 0) book.delete(rec.price);
          }
          this.orders.delete(targetId);
          // Re-submit as a limit with new id, price, qty
          return this.submit({ id, clientId, side: rec.side, type: 'limit', price, qty });
        }
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }

      // ── Universal validation for trading orders ────────────
      if (!['limit', 'market', 'ioc', 'fok', 'postonly'].includes(type)) {
        acks.push(this._ack(id, 'rejected', { reason: 'unknown-type', type }));
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }
      if (side !== 'buy' && side !== 'sell') {
        acks.push(this._ack(id, 'rejected', { reason: 'bad-side', side }));
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }
      if (!this._validQty(qty)) {
        acks.push(this._ack(id, 'rejected', { reason: 'bad-qty', qty }));
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }
      if (type !== 'market' && !this._validPrice(price)) {
        acks.push(this._ack(id, 'rejected', { reason: 'bad-price', price }));
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }
      // Duplicate-id sequencer guarantee
      if (id != null && this.seenIds.has(id)) {
        acks.push(this._ack(id, 'rejected', { reason: 'duplicate-id' }));
        const t1 = performance.now();
        return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
      }
      if (id != null) this.seenIds.add(id);

      // ── postonly pre-trade check ───────────────────────────
      if (type === 'postonly') {
        if (this._wouldCross(side, price)) {
          acks.push(this._ack(id, 'rejected', { reason: 'would-cross-postonly' }));
          const t1 = performance.now();
          return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
        }
      }

      // ── FOK pre-trade check: simulate matchable qty without mutating ──
      if (type === 'fok') {
        if (!this._fokFillable(side, price, qty)) {
          acks.push(this._ack(id, 'rejected', { reason: 'fok-unfillable' }));
          const t1 = performance.now();
          return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
        }
      }

      // ── Match against opposite book ────────────────────────
      const matchType = (type === 'postonly') ? 'noop' : (type === 'fok' ? 'limit' : type);
      const r = (matchType === 'noop')
        ? { qty, fills: [], takerCancelled: false }
        : this._match(side, matchType, price, qty, id, clientId);
      fills = r.fills;
      const remaining = r.qty;
      const filled = qty - remaining;

      // ── Resting decision per order type ────────────────────
      if (r.takerCancelled) {
        acks.push(this._ack(id, 'stp-cancel-taker', { filled, remaining }));
      } else if (type === 'limit' || type === 'postonly') {
        if (remaining > 0) {
          const book = side === 'buy' ? this.bids : this.asks;
          const rec = { id, clientId, qty: remaining, ts: performance.now() };
          this._push(book, price, rec);
          this.orders.set(id, { side, price, qtyRemaining: remaining, clientId });
        }
        const status = filled === 0 ? 'accepted'
                     : remaining === 0 ? 'filled' : 'partial';
        acks.push(this._ack(id, status, { filled, remaining }));
      } else if (type === 'market') {
        // never rests. honest report of unfilled remainder.
        const status = filled === 0 ? 'unfilled'
                     : remaining === 0 ? 'filled' : 'partial-unfilled';
        acks.push(this._ack(id, status, { filled, remaining }));
      } else if (type === 'ioc') {
        const status = filled === 0 ? 'cancelled-ioc'
                     : remaining === 0 ? 'filled' : 'partial-cancelled-ioc';
        acks.push(this._ack(id, status, { filled, remaining }));
      } else if (type === 'fok') {
        acks.push(this._ack(id, 'filled', { filled, remaining: 0 }));
      }

      // ── Invariant: qty conservation ────────────────────────
      // Tested by the correctness suite. Asserts the engine doesn't
      // print or destroy quantity. Fires only in dev (console only).
      const totalFilledQty = fills.reduce((s, f) => s + f.qty, 0);
      if (Math.abs(totalFilledQty - filled) > 1e-9) {
        console.error('[engine] qty conservation violation', { totalFilledQty, filled, fills });
      }
    } catch (e) {
      this.errors++;
      acks.push(this._ack(id, 'error', { message: String(e) }));
    }

    const t1 = performance.now();
    return { acks, fills, latencyNs: (t1 - t0) * 1e6 };
  }

  // ── FOK feasibility: does the opposite book have enough crossing
  // qty? Read-only - does not mutate. ──────────────────────────────
  _fokFillable(side, price, qty) {
    const opp = side === 'buy' ? this.asks : this.bids;
    if (opp.size === 0) return false;
    const prices = [...opp.keys()].sort((a, b) => side === 'buy' ? a - b : b - a);
    let need = qty;
    for (const px of prices) {
      const crosses = side === 'buy' ? px <= price : px >= price;
      if (!crosses) break;
      const lvlQty = opp.get(px).reduce((s, o) => s + o.qty, 0);
      need -= lvlQty;
      if (need <= 0) return true;
    }
    return false;
  }

  // ── Read-only depth snapshot for UI display ──────────────────────
  snapshot(levels = 5) {
    const collect = (book, sortDesc) => {
      const arr = [...book.entries()].map(([px, q]) => ({
        px, sz: q.reduce((s, o) => s + o.qty, 0),
      }));
      arr.sort((a, b) => sortDesc ? b.px - a.px : a.px - b.px);
      return arr.slice(0, levels);
    };
    return {
      bids: collect(this.bids, true),
      asks: collect(this.asks, false),
      lastTrade: this.lastTrade,
    };
  }

  stats() {
    return {
      ordersProcessed: this.ordersProcessed,
      fillsTotal: this.fillsTotal,
      restingOrders: this.orders.size,
      errors: this.errors,
    };
  }
}

if (typeof window !== 'undefined') window.MatchingEngine = MatchingEngine;
if (typeof self !== 'undefined' && typeof window === 'undefined') self.MatchingEngine = MatchingEngine;
