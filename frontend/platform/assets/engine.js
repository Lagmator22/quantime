/* =====================================================================
   IICPC PLATFORM · REFERENCE MATCHING ENGINE
   ---------------------------------------------------------------------
   A real, working price-time priority orderbook + matching engine
   written in plain JS. The platform uses this as the "default sandbox"
   target: bots send orders to it, we measure real latency, and the
   numbers you see on screen are genuine — not random.

   When a contestant uploads code, we hash + register their submission
   metadata (we can't actually run their C++/Rust binary in the
   browser), but the stress run still produces real data because it
   runs against this engine. In a real cloud deployment, swap this
   module for an HTTP/WS client that POSTs orders to the contestant's
   container.

   Public API:
     const eng = new MatchingEngine();
     eng.submit({ side: 'buy'|'sell', type: 'limit'|'market'|'cancel',
                  price, qty, id, ts });
     // returns { acks, fills, latencyNs }
     eng.snapshot()    -> { bids:[{px,sz}], asks:[{px,sz}], lastTrade }
     eng.stats()       -> { ordersProcessed, fillsTotal, ... }
     eng.reset()
===================================================================== */
class MatchingEngine {
  constructor() { this.reset(); }
  reset() {
    this.bids = new Map();   // price -> [{id, qty, ts}]
    this.asks = new Map();
    this.orders = new Map(); // id -> {side, price, qtyRemaining}
    this.ordersProcessed = 0;
    this.fillsTotal = 0;
    this.lastTrade = null;
    this.errors = 0;
  }

  // —— Insert into a price-keyed FIFO queue (price-time priority) —
  _push(book, price, order) {
    if (!book.has(price)) book.set(price, []);
    book.get(price).push(order);
  }

  // —— Best price helpers ————————————————————————————————
  _best(book, isBids) {
    if (book.size === 0) return null;
    let best = null;
    for (const px of book.keys()) {
      if (best === null) best = px;
      else if (isBids ? px > best : px < best) best = px;
    }
    return best;
  }

  // —— Match incoming order against opposite book —————————————
  _match(side, type, price, qty, id) {
    const fills = [];
    const opp = side === 'buy' ? this.asks : this.bids;
    const isCross = (oppPx) =>
      type === 'market' ? true
      : side === 'buy' ? oppPx <= price
      : oppPx >= price;

    while (qty > 0) {
      const bestPx = this._best(opp, side === 'sell');
      if (bestPx === null || !isCross(bestPx)) break;
      const queue = opp.get(bestPx);
      while (queue.length && qty > 0) {
        const resting = queue[0];
        const matched = Math.min(qty, resting.qty);
        qty -= matched;
        resting.qty -= matched;
        fills.push({ price: bestPx, qty: matched, takerId: id, makerId: resting.id });
        this.fillsTotal++;
        this.lastTrade = { px: bestPx, qty: matched, ts: performance.now() };
        if (resting.qty === 0) {
          queue.shift();
          this.orders.delete(resting.id);
        }
      }
      if (queue.length === 0) opp.delete(bestPx);
    }
    return { qty, fills };
  }

  // —— Main entry: process one order, return result + latency ——
  submit(order) {
    const t0 = performance.now();
    this.ordersProcessed++;
    const { side, type, price, qty, id } = order;

    let acks = [];
    let fills = [];

    try {
      if (type === 'cancel') {
        const rec = this.orders.get(order.targetId);
        if (rec) {
          const book = rec.side === 'buy' ? this.bids : this.asks;
          const queue = book.get(rec.price);
          if (queue) {
            const idx = queue.findIndex(o => o.id === order.targetId);
            if (idx >= 0) {
              queue.splice(idx, 1);
              if (queue.length === 0) book.delete(rec.price);
            }
          }
          this.orders.delete(order.targetId);
          acks.push({ id, status: 'cancelled' });
        } else {
          acks.push({ id, status: 'cancel-miss' });
        }
      } else {
        const result = this._match(side, type, price, qty, id);
        fills = result.fills;
        const remaining = result.qty;
        if (type === 'limit' && remaining > 0) {
          const book = side === 'buy' ? this.bids : this.asks;
          const rec = { id, qty: remaining, ts: performance.now() };
          this._push(book, price, rec);
          this.orders.set(id, { side, price, qtyRemaining: remaining });
        }
        acks.push({ id, status: 'accepted', filled: qty - remaining, remaining });
      }
    } catch (e) {
      this.errors++;
      acks.push({ id, status: 'error', message: String(e) });
    }

    const t1 = performance.now();
    return { acks, fills, latencyNs: (t1 - t0) * 1e6 }; // ms→ns
  }

  // —— Read-only depth snapshot for UI display ——————————————
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
