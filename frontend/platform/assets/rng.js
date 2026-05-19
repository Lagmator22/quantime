/* =====================================================================
   IICPC PLATFORM · SEEDED RANDOM (deterministic replay)
   ---------------------------------------------------------------------
   Math.random() can't be seeded, so we ship a tiny PRNG so runs are
   replayable: same seed in → byte-for-byte identical bot traffic out.

   Algorithm: xoshiro128**  (Vigna, 2018). Fast, no warm-up, period 2^128-1.
   Output: [0,1) double, like Math.random().

   API:
     const rng = new RNG(seed);
     rng.next();              // float in [0,1)
     rng.int(n);              // integer in [0,n)
     rng.range(lo, hi);       // float in [lo,hi)
     rng.pick(arr);           // random element
     rng.seed                 // current seed (read-only)
===================================================================== */
class RNG {
  constructor(seed = Date.now() & 0xffffffff) {
    this.seed = seed >>> 0;
    // Init state via splitmix32 from the seed
    let s = this.seed;
    const split = () => {
      s = (s + 0x9e3779b9) >>> 0;
      let z = s;
      z = Math.imul(z ^ (z >>> 16), 0x85ebca6b) >>> 0;
      z = Math.imul(z ^ (z >>> 13), 0xc2b2ae35) >>> 0;
      return (z ^ (z >>> 16)) >>> 0;
    };
    this.s0 = split() || 1;
    this.s1 = split() || 1;
    this.s2 = split() || 1;
    this.s3 = split() || 1;
  }

  next() {
    const rot = (x, k) => ((x << k) | (x >>> (32 - k))) >>> 0;
    const result = (rot(Math.imul(this.s1, 5) >>> 0, 7) >>> 0) * 9;
    const t = (this.s1 << 9) >>> 0;
    this.s2 = (this.s2 ^ this.s0) >>> 0;
    this.s3 = (this.s3 ^ this.s1) >>> 0;
    this.s1 = (this.s1 ^ this.s2) >>> 0;
    this.s0 = (this.s0 ^ this.s3) >>> 0;
    this.s2 = (this.s2 ^ t) >>> 0;
    this.s3 = rot(this.s3, 11);
    return (result >>> 0) / 4294967296;
  }

  int(n) { return Math.floor(this.next() * n); }
  range(lo, hi) { return lo + this.next() * (hi - lo); }
  pick(arr) { return arr[this.int(arr.length)]; }
}

if (typeof window !== 'undefined') window.RNG = RNG;
if (typeof self !== 'undefined' && typeof window === 'undefined') self.RNG = RNG;
