/* =====================================================================
   IICPC PLATFORM · LIVE MARKET DATA FEED (Binance)
   ---------------------------------------------------------------------
   Optional realistic order-flow source. Subscribes to Binance's public
   WebSocket trade stream for any symbol (default BTCUSDT) and emits
   normalised events. Bots in run.html can use this as a seed for
   their order generation so the test flow resembles real markets.

   No auth required for public streams. Spec:
     https://binance-docs.github.io/apidocs/spot/en/#websocket-market-streams

   API:
     const feed = new MarketFeed('btcusdt');
     feed.onTrade(t => …);            // {ts, price, qty, side}
     feed.onBookTicker(b => …);       // {bid, ask, bidSz, askSz}
     feed.connect();  feed.close();

   If the WS connection fails (CORS in some sandboxes, network down),
   the feed silently no-ops - caller just gets no callbacks. Run pages
   should treat this feed as a NICE-TO-HAVE and fall back to synthetic
   order generation when no events arrive within 3s.
===================================================================== */
class MarketFeed {
  constructor(symbol = 'btcusdt') {
    this.symbol = symbol.toLowerCase();
    this.ws = null;
    this.tradeCbs = [];
    this.bookCbs = [];
    this.connected = false;
    this.lastTradeTs = 0;
  }

  connect() {
    if (this.ws) return;
    const url = `wss://stream.binance.com:9443/stream?streams=${this.symbol}@trade/${this.symbol}@bookTicker`;
    try {
      this.ws = new WebSocket(url);
    } catch (e) {
      console.warn('[MarketFeed] ws create failed', e);
      return;
    }
    this.ws.addEventListener('open', () => {
      this.connected = true;
      console.log('[MarketFeed] connected ·', this.symbol);
    });
    this.ws.addEventListener('message', (e) => {
      try {
        const msg = JSON.parse(e.data);
        const data = msg.data;
        if (!data) return;
        if (data.e === 'trade') {
          this.lastTradeTs = Date.now();
          const t = {
            ts: data.T,
            price: parseFloat(data.p),
            qty: parseFloat(data.q),
            side: data.m ? 'sell' : 'buy',   // m=true: maker is buyer → trade was a sell
          };
          this.tradeCbs.forEach(cb => { try { cb(t); } catch (_) {} });
        } else if (data.u !== undefined && data.b !== undefined) {
          // bookTicker
          const b = {
            ts: Date.now(),
            bid: parseFloat(data.b),
            bidSz: parseFloat(data.B),
            ask: parseFloat(data.a),
            askSz: parseFloat(data.A),
          };
          this.bookCbs.forEach(cb => { try { cb(b); } catch (_) {} });
        }
      } catch (_) {}
    });
    this.ws.addEventListener('close', () => { this.connected = false; });
    this.ws.addEventListener('error', () => { this.connected = false; });
  }

  isStale(ms = 3000) {
    return !this.connected || (Date.now() - this.lastTradeTs > ms);
  }

  onTrade(cb) { this.tradeCbs.push(cb); }
  onBookTicker(cb) { this.bookCbs.push(cb); }

  close() {
    try { this.ws && this.ws.close(); } catch {}
    this.ws = null;
    this.connected = false;
  }
}

if (typeof window !== 'undefined') window.MarketFeed = MarketFeed;
