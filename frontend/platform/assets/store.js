/* =====================================================================
   QuanTime · PERSISTENCE STORE
   ---------------------------------------------------------------------
   Tiny localStorage wrapper for cross-page state. IndexedDB is used
   for binary uploads (see uploadBinary below). Everything namespaced
   under "iicpc.*" so it doesn't collide with other apps.

   API:
     Store.get(key, fallback)
     Store.set(key, value)
     Store.patch(key, partial)        // shallow merge for object values
     Store.delete(key)
     Store.list(prefix)               // returns matching keys
     Store.uploadBinary(id, blob)     // IndexedDB
     Store.fetchBinary(id)            // returns Blob | null
     Store.subscribe(key, cb)         // cross-tab updates via 'storage' event
     Store.broadcast(channel, msg)    // BroadcastChannel realtime sync
     Store.onBroadcast(channel, cb)

   Schema we use across pages:
     iicpc.team          { id, name, members[] }
     iicpc.submissions   [{ id, name, lang, size, status, hash, createdAt, runs[] }]
     iicpc.runs          [{ id, submissionId, teamId, status, metrics, log[], finishedAt }]
     iicpc.leaderboard   [{ teamId, name, score, p50, p99, tps, err, lastRun }]
     iicpc.judges        [{ id, name }]
     iicpc.config        { weights, deadlines, ... }
===================================================================== */
(function () {
  const NS = 'iicpc.';

  // -- localStorage wrapper -------------------------------------
  const Store = {
    get(key, fallback = null) {
      try {
        const raw = localStorage.getItem(NS + key);
        return raw ? JSON.parse(raw) : fallback;
      } catch (e) {
        console.error('[Store.get]', key, e);
        return fallback;
      }
    },
    set(key, value) {
      try {
        localStorage.setItem(NS + key, JSON.stringify(value));
        // Same-tab listeners - 'storage' only fires cross-tab
        Store._fire(key, value);
      } catch (e) {
        console.error('[Store.set]', key, e);
      }
    },
    patch(key, partial) {
      const cur = Store.get(key, {});
      Store.set(key, { ...cur, ...partial });
    },
    delete(key) {
      localStorage.removeItem(NS + key);
      Store._fire(key, null);
    },
    list(prefix = '') {
      const out = [];
      for (let i = 0; i < localStorage.length; i++) {
        const k = localStorage.key(i);
        if (k && k.startsWith(NS + prefix)) out.push(k.slice(NS.length));
      }
      return out;
    },
    push(key, item) {
      const arr = Store.get(key, []);
      arr.push(item);
      Store.set(key, arr);
      return arr;
    },
    update(key, predicate, mutator) {
      const arr = Store.get(key, []);
      const idx = arr.findIndex(predicate);
      if (idx >= 0) {
        arr[idx] = { ...arr[idx], ...mutator(arr[idx]) };
        Store.set(key, arr);
      }
      return arr;
    },

    // -- cross-tab + same-tab subscriber ------------------
    _subs: {},
    _fire(key, value) {
      (Store._subs[key] || []).forEach(cb => { try { cb(value); } catch (e) {} });
    },
    subscribe(key, cb) {
      Store._subs[key] = Store._subs[key] || [];
      Store._subs[key].push(cb);
      return () => {
        Store._subs[key] = (Store._subs[key] || []).filter(x => x !== cb);
      };
    },

    // -- BroadcastChannel: realtime cross-tab events ------
    _channels: {},
    _channel(name) {
      if (!Store._channels[name]) {
        Store._channels[name] = new BroadcastChannel('iicpc.' + name);
      }
      return Store._channels[name];
    },
    broadcast(name, msg) {
      Store._channel(name).postMessage(msg);
    },
    onBroadcast(name, cb) {
      const ch = Store._channel(name);
      const handler = (e) => cb(e.data);
      ch.addEventListener('message', handler);
      return () => ch.removeEventListener('message', handler);
    },

    // -- IndexedDB for binary uploads ----------------------
    _db: null,
    async _openDB() {
      if (Store._db) return Store._db;
      return new Promise((resolve, reject) => {
        const req = indexedDB.open('iicpc-uploads', 1);
        req.onupgradeneeded = () => {
          req.result.createObjectStore('blobs');
        };
        req.onsuccess = () => { Store._db = req.result; resolve(Store._db); };
        req.onerror = () => reject(req.error);
      });
    },
    async uploadBinary(id, blob) {
      const db = await Store._openDB();
      return new Promise((resolve, reject) => {
        const tx = db.transaction('blobs', 'readwrite');
        tx.objectStore('blobs').put(blob, id);
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
      });
    },
    async fetchBinary(id) {
      const db = await Store._openDB();
      return new Promise((resolve) => {
        const tx = db.transaction('blobs', 'readonly');
        const req = tx.objectStore('blobs').get(id);
        req.onsuccess = () => resolve(req.result || null);
        req.onerror = () => resolve(null);
      });
    },
  };

  // Cross-tab storage events → fire local subscribers
  window.addEventListener('storage', (e) => {
    if (e.key && e.key.startsWith(NS)) {
      const key = e.key.slice(NS.length);
      let value;
      try { value = e.newValue ? JSON.parse(e.newValue) : null; } catch {}
      Store._fire(key, value);
    }
  });

  // -- Seed defaults if first visit ---------------------
  if (!Store.get('config')) {
    Store.set('config', {
      // Composite scoring weights - edit to retune
      weights: { speed: 0.4, throughput: 0.4, correctness: 0.2 },
      // Sandbox limits mirror the real server-side container caps
      // (see docs.html · Submission sandbox).
      sandbox: { cpu: 1, memMB: 256, timeoutS: 60 },
    });
  }
  if (!Store.get('team')) {
    Store.set('team', {
      id: 't_' + Math.random().toString(36).slice(2, 10),
      name: 'unnamed-team',
      members: [{ name: 'You', role: 'captain' }],
      createdAt: Date.now(),
    });
  }
  if (!Store.get('submissions')) Store.set('submissions', []);
  if (!Store.get('runs')) Store.set('runs', []);
  if (!Store.get('leaderboard')) {
    // Seed sample teams so the leaderboard isn't empty before a real run.
    // Shown only when the backend is offline (the live bridge replaces these).
    // p50/p99 are nanoseconds (the render path divides by 1e6 → ms); err is a
    // fraction. Values track the landing page's honest p50 ~0.2ms / p99 ~10-32ms.
    Store.set('leaderboard', [
      { teamId: 'parity-bit',    name: 'parity-bit',    region: 'tokyo',     score: 184, p50: 180000, p99: 9500000,  tps: 2410000, err: 0.002, lastRun: Date.now() - 12000 },
      { teamId: 'cache-warmers', name: 'cache-warmers', region: 'berlin',    score: 171, p50: 210000, p99: 16800000, tps: 2180000, err: 0.005, lastRun: Date.now() - 30000 },
      { teamId: 'fix-or-die',    name: 'fix-or-die',    region: 'london',    score: 162, p50: 240000, p99: 19500000, tps: 1980000, err: 0.004, lastRun: Date.now() - 45000 },
      { teamId: 'lockfree.lol',  name: 'lockfree.lol',  region: 'nyc',       score: 149, p50: 270000, p99: 21000000, tps: 1820000, err: 0.011, lastRun: Date.now() - 60000 },
      { teamId: 'hot-path',      name: 'hot-path',      region: 'sf',        score: 138, p50: 310000, p99: 24500000, tps: 1640000, err: 0.008, lastRun: Date.now() - 90000 },
      { teamId: 'epoch.gg',      name: 'epoch.gg',      region: 'singapore', score: 124, p50: 350000, p99: 28200000, tps: 1510000, err: 0.014, lastRun: Date.now() - 120000 },
      { teamId: 'rdtsc-rd',      name: 'rdtsc-rd',      region: 'mumbai',    score: 109, p50: 410000, p99: 31800000, tps: 1390000, err: 0.019, lastRun: Date.now() - 180000 },
    ]);
  }

  window.Store = Store;
})();
