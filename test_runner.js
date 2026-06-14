const fs = require('fs');

const engineCode = fs.readFileSync('./frontend/platform/assets/engine.js', 'utf8');
const correctnessCode = fs.readFileSync('./frontend/platform/assets/correctness.js', 'utf8');

// Mock browser environment
global.window = {};
global.self = {};
global.performance = { now: () => Date.now() };

eval(engineCode);
eval(correctnessCode);

const rt = {
  eng: null,
  async close() {},
  async submit(order) {
    return this.eng.submit(order);
  }
};

window.Correctness.runSuite(
  async () => {
    rt.eng = new window.MatchingEngine();
    return rt;
  }
).then(res => {
  console.log("Score:", res.score);
  for (const r of res.results) {
    if (!r.passed) {
      console.log(`[FAILED] ${r.name}: ${r.details}`);
    }
  }
});
