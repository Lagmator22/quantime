/* =====================================================================
   IICPC PLATFORM · BOT WORKER (Web Worker)
   ---------------------------------------------------------------------
   Spawned from run.html. Each worker simulates a fleet of bots that
   generate orders against the matching engine. Workers post telemetry
   back to the main thread on every batch. Why a Worker? So the main
   UI stays responsive during heavy load, just like real bot processes
   running on separate cores in production.

   Protocol:
     main → worker:  { type: 'start', config: {...} }
     main → worker:  { type: 'stop' }
     worker → main:  { type: 'tele', batch: [...] }
     worker → main:  { type: 'log', level, msg }
     worker → main:  { type: 'done', stats }
===================================================================== */
importScripts('engine.js');

let engine = null;
let running = false;
let cfg = null;
let tickHandle = null;

// —— Strategy templates: maker, taker, sniper, canceller ————————
// Each strategy returns the next order this bot will send.
const strategies = {
  // Random LIMIT around mid; provides liquidity (maker)
  maker(state) {
    const mid = state.mid;
    const side = Math.random() < 0.5 ? 'buy' : 'sell';
    const offset = 1 + Math.floor(Math.random() * 10);
    const price = side === 'buy' ? mid - offset * 0.25 : mid + offset * 0.25;
    return {
      id: ++state.idSeq,
      side, type: 'limit',
      price: +price.toFixed(2),
      qty: +(Math.random() * 5 + 1).toFixed(2),
    };
  },
  // MARKET orders — takes liquidity, drives matches
  taker(state) {
    return {
      id: ++state.idSeq,
      side: Math.random() < 0.5 ? 'buy' : 'sell',
      type: 'market',
      price: 0,
      qty: +(Math.random() * 8 + 0.5).toFixed(2),
    };
  },
  // Aggressive crossing limits — heavy fills
  sniper(state) {
    const side = Math.random() < 0.5 ? 'buy' : 'sell';
    const price = side === 'buy' ? state.mid + 1.0 : state.mid - 1.0;
    return {
      id: ++state.idSeq,
      side, type: 'limit',
      price: +price.toFixed(2),
      qty: +(Math.random() * 15 + 1).toFixed(2),
    };
  },
};

function processBatch() {
  if (!running) return;
  const t0 = performance.now();
  const batchSize = cfg.opsPerTick;
  const latencies = [];
  let fills = 0, errs = 0;

  for (let i = 0; i < batchSize; i++) {
    // pick a strategy weighted by config
    const r = Math.random();
    const strat =
      r < cfg.mix.maker ? 'maker'
      : r < cfg.mix.maker + cfg.mix.taker ? 'taker'
      : 'sniper';
    const order = strategies[strat](cfg.state);
    const res = engine.submit(order);
    latencies.push(res.latencyNs);
    fills += res.fills.length;
    if (res.acks[0]?.status === 'error') errs++;

    // Drift mid randomly so the book moves
    cfg.state.mid += (Math.random() - 0.5) * 0.05;
  }

  const wallMs = performance.now() - t0;
  postMessage({
    type: 'tele',
    batch: {
      ts: Date.now(),
      ops: batchSize,
      wallMs,
      latencies,        // ns per order
      fills,
      errors: errs,
      ordersProcessed: engine.ordersProcessed,
      restingOrders: engine.orders.size,
    },
  });

  // Rate-limit: aim for cfg.tickHz ticks per second
  const targetMs = 1000 / cfg.tickHz;
  const sleep = Math.max(0, targetMs - wallMs);
  tickHandle = setTimeout(processBatch, sleep);
}

self.addEventListener('message', (e) => {
  const m = e.data;
  if (m.type === 'start') {
    cfg = {
      botCount: m.config.botCount || 1000,
      opsPerTick: m.config.opsPerTick || 200,
      tickHz: m.config.tickHz || 20,
      mix: m.config.mix || { maker: 0.5, taker: 0.35, sniper: 0.15 },
      state: { idSeq: 0, mid: m.config.startMid || 3142.50 },
    };
    engine = new MatchingEngine();
    running = true;
    postMessage({ type: 'log', level: 'ok', msg: `worker[${m.workerId}] online · ${cfg.botCount} bots · ${cfg.tickHz}Hz` });
    processBatch();
  } else if (m.type === 'stop') {
    running = false;
    if (tickHandle) clearTimeout(tickHandle);
    postMessage({
      type: 'done',
      stats: engine ? engine.stats() : null,
    });
  }
});
