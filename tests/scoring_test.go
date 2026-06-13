// Unit tests for the composite scoring functions.
// Verifies mathExpDecay and mathSat produce correct scores.
// Run with: go test -v ./tests/ -run TestScoring
// No external dependencies required.
package tests

import (
	"math"
	"testing"
)

// Mirrors telemetry's mathExpDecay: score = 1 / (1 + x/k)
func mathExpDecay(x, k float64) float64 {
	if x <= 0 {
		return 1
	}
	return 1.0 / (1.0 + x/k)
}

// Mirrors telemetry's mathSat: score = min(x/k, 1)
func mathSat(x, k float64) float64 {
	v := x / k
	if v > 1 {
		return 1
	}
	return v
}

// compositeScore mirrors the telemetry scoring formula.
// Weights: 40% speed, 40% throughput, 20% correctness.
func compositeScore(p99Ns, tps, errRate float64) float64 {
	speedScore := 100.0 * mathExpDecay(p99Ns, 200_000_000)
	tputScore := 100.0 * mathSat(tps, 200_000)
	correctnessScore := 100.0 * (1 - errRate)
	return 0.4*speedScore + 0.4*tputScore + 0.2*correctnessScore
}

func TestExpDecay_ZeroLatency(t *testing.T) {
	// Zero latency should give perfect score (1.0)
	got := mathExpDecay(0, 200_000_000)
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestExpDecay_NegativeLatency(t *testing.T) {
	// Negative latency should clamp to perfect score
	got := mathExpDecay(-100, 200_000_000)
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestExpDecay_AtK(t *testing.T) {
	// When x == k, score should be 0.5
	got := mathExpDecay(200_000_000, 200_000_000)
	if math.Abs(got-0.5) > 0.001 {
		t.Errorf("expected ~0.5, got %f", got)
	}
}

func TestExpDecay_VeryHighLatency(t *testing.T) {
	// 10x the decay constant should give low score
	got := mathExpDecay(2_000_000_000, 200_000_000)
	if got > 0.15 {
		t.Errorf("expected <0.15 for very high latency, got %f", got)
	}
}

func TestSat_ZeroTPS(t *testing.T) {
	got := mathSat(0, 200_000)
	if got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestSat_HalfCap(t *testing.T) {
	got := mathSat(100_000, 200_000)
	if math.Abs(got-0.5) > 0.001 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestSat_AtCap(t *testing.T) {
	got := mathSat(200_000, 200_000)
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestSat_OverCap(t *testing.T) {
	// Beyond cap should still be 1.0 (clamped)
	got := mathSat(500_000, 200_000)
	if got != 1.0 {
		t.Errorf("expected 1.0 (clamped), got %f", got)
	}
}

func TestComposite_PerfectRun(t *testing.T) {
	// Zero latency, max throughput, no errors
	score := compositeScore(0, 200_000, 0)
	if math.Abs(score-100.0) > 0.001 {
		t.Errorf("perfect run should score 100, got %f", score)
	}
}

func TestComposite_ZeroEverything(t *testing.T) {
	// Zero latency, zero throughput, no errors
	// Speed: 100*1 = 100, TPS: 100*0 = 0, Correctness: 100*1 = 100
	// Composite: 0.4*100 + 0.4*0 + 0.2*100 = 60
	score := compositeScore(0, 0, 0)
	if math.Abs(score-60.0) > 0.001 {
		t.Errorf("zero throughput should score 60, got %f", score)
	}
}

func TestComposite_AllErrors(t *testing.T) {
	// Good speed and throughput but 100% error rate
	// Speed: 100*0.5 = 50, TPS: 100*1 = 100, Correctness: 100*0 = 0
	// Composite: 0.4*50 + 0.4*100 + 0.2*0 = 60
	score := compositeScore(200_000_000, 200_000, 1.0)
	if math.Abs(score-60.0) > 0.001 {
		t.Errorf("all errors should score 60, got %f", score)
	}
}

func TestComposite_RealisticRun(t *testing.T) {
	// Realistic values: 5ms p99, 50k tps, 2% error rate
	score := compositeScore(5_000_000, 50_000, 0.02)

	// Speed: 100 * 1/(1 + 5e6/2e8) = 100 * 1/1.025 = 97.56
	// TPS: 100 * 50k/200k = 25
	// Correctness: 100 * 0.98 = 98
	// Composite: 0.4*97.56 + 0.4*25 + 0.2*98 = 39.02 + 10 + 19.6 = 68.62
	if score < 60 || score > 75 {
		t.Errorf("realistic run should be 60-75 range, got %f", score)
	}
}

func TestComposite_HighLatencyRun(t *testing.T) {
	// Bad p99: 500ms, decent tps, low errors
	score := compositeScore(500_000_000, 100_000, 0.01)

	// Speed: 100 * 1/(1 + 500e6/200e6) = 100 * 1/3.5 = 28.57
	// TPS: 100 * 100k/200k = 50
	// Correctness: 100 * 0.99 = 99
	// Composite: 0.4*28.57 + 0.4*50 + 0.2*99 = 11.43 + 20 + 19.8 = 51.23
	if score < 45 || score > 60 {
		t.Errorf("high latency should score 45-60, got %f", score)
	}
}
