/* =====================================================================
   IICPC PLATFORM · T-DIGEST STREAMING PERCENTILES
   ---------------------------------------------------------------------
   At 1M ops/s, sorting 1M floats every 100ms to compute p50/p99 costs
   ~50ms by itself and dominates the CPU budget. t-digest is a
   sketch data structure that gives accurate percentiles in O(log N)
   per insert with constant memory.

   Reference: Dunning & Ertl, "Computing Extremely Accurate Quantiles
   Using t-Digests", https://arxiv.org/abs/1902.04023

   This is a simplified, dependency-free implementation tuned for our
   use case (latency µs, mostly-monotonic stream, p50/p90/p99 queries).
   Accuracy at p99 with compression=200 is typically within 0.1%.

   API:
     const td = new TDigest(compression=200);
     td.add(value, weight=1);
     td.quantile(p);            // p in [0,1]
     td.merge(other);           // combine two digests
     td.count;                  // total weight
     td.reset();
     td.toJSON() / TDigest.fromJSON(o);

   Edge cases handled:
     • NaN/Infinity inputs → ignored
     • Empty digest → quantile() returns NaN
     • Single value → quantile() returns that value
     • Negative weights → coerced to 1
===================================================================== */
class Centroid {
  constructor(mean, weight) { this.mean = mean; this.weight = weight; }
}

class TDigest {
  constructor(compression = 200) {
    this.compression = compression;
    this.centroids = [];   // sorted by mean
    this.count = 0;
    this.unmerged = [];    // staging buffer for batch merges
    this.unmergedWeight = 0;
    this.BUFFER = Math.max(50, compression * 5);
  }

  reset() {
    this.centroids = [];
    this.unmerged = [];
    this.count = 0;
    this.unmergedWeight = 0;
  }

  add(value, weight = 1) {
    if (!Number.isFinite(value)) return;       // drop NaN/Inf
    if (!(weight > 0)) weight = 1;
    this.unmerged.push(new Centroid(value, weight));
    this.unmergedWeight += weight;
    if (this.unmerged.length >= this.BUFFER) this._mergeBuffer();
  }

  // —— Merge staged points into the main centroid array ——————
  _mergeBuffer() {
    if (this.unmerged.length === 0) return;
    const all = this.centroids.concat(this.unmerged);
    all.sort((a, b) => a.mean - b.mean);
    this.count += this.unmergedWeight;
    this.unmerged = [];
    this.unmergedWeight = 0;

    const compressed = [];
    const cumulative = [];
    let sum = 0;
    for (const c of all) { sum += c.weight; cumulative.push(sum); }

    // The k-function controls how many points fit per centroid.
    // We use the scale function k_1 from the paper.
    const total = this.count;
    const compression = this.compression;
    const kFromQ = (q) => compression * (Math.asin(2 * q - 1) / Math.PI + 0.5);
    const qFromK = (k) => 0.5 * (Math.sin(Math.PI * (k / compression - 0.5)) + 1);

    let cur = all[0];
    let qStart = 0;
    let kStart = kFromQ(qStart);
    let qLimit = qFromK(kStart + 1);

    for (let i = 1; i < all.length; i++) {
      const next = all[i];
      const qCandidate = (cumulative[i - 1] + next.weight) / total;
      if (qCandidate <= qLimit) {
        // Merge into current centroid
        const newWeight = cur.weight + next.weight;
        cur.mean = cur.mean + (next.mean - cur.mean) * next.weight / newWeight;
        cur.weight = newWeight;
      } else {
        compressed.push(cur);
        cur = new Centroid(next.mean, next.weight);
        qStart = (cumulative[i - 1]) / total;
        kStart = kFromQ(qStart);
        qLimit = qFromK(kStart + 1);
      }
    }
    compressed.push(cur);
    this.centroids = compressed;
  }

  // —— Quantile query —————————————————————————————————————
  quantile(p) {
    if (this.unmerged.length > 0) this._mergeBuffer();
    if (this.centroids.length === 0) return NaN;
    if (this.centroids.length === 1) return this.centroids[0].mean;
    p = Math.max(0, Math.min(1, p));
    const target = p * this.count;

    let cum = 0;
    for (let i = 0; i < this.centroids.length; i++) {
      const c = this.centroids[i];
      const cumPrev = cum;
      cum += c.weight;
      if (target <= cum) {
        // Linear interpolation between this centroid's mean and the next
        if (i === 0 || i === this.centroids.length - 1) return c.mean;
        const prev = this.centroids[i - 1];
        const next = this.centroids[i + 1];
        const range = (next.mean - prev.mean) / 2;
        const fraction = (target - cumPrev) / c.weight;
        return prev.mean + (c.mean - prev.mean) / 2 + fraction * range;
      }
    }
    return this.centroids[this.centroids.length - 1].mean;
  }

  // —— Merge another digest in (for cross-worker aggregation) ——
  merge(other) {
    if (!other || other.centroids.length === 0) return;
    for (const c of other.centroids) this.add(c.mean, c.weight);
    for (const c of other.unmerged) this.add(c.mean, c.weight);
  }

  toJSON() {
    return {
      compression: this.compression,
      count: this.count,
      centroids: this.centroids.map(c => [c.mean, c.weight]),
    };
  }

  static fromJSON(obj) {
    const t = new TDigest(obj.compression || 200);
    t.count = obj.count || 0;
    t.centroids = (obj.centroids || []).map(([m, w]) => new Centroid(m, w));
    return t;
  }
}

// Expose in both window + worker scopes
if (typeof window !== 'undefined') window.TDigest = TDigest;
if (typeof self !== 'undefined' && typeof window === 'undefined') self.TDigest = TDigest;
