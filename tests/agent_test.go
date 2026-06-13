// Unit tests for the AI analyzer agent system.
// Tests the synthesizer logic, severity scoring, and report generation.
// Run with: go test -v ./tests/ -run TestAgent
// No Gemini API key needed: these test the local processing logic.
package tests

import (
	"testing"
)

// Mirrors agents.severityWeight for standalone testing
var severityWeight = map[string]int{
	"critical": 25,
	"high":     15,
	"medium":   8,
	"low":      3,
	"info":     1,
}

// Finding mirrors agents.Finding
type agentFinding struct {
	Severity    string
	Category    string
	Location    string
	Description string
	Suggestion  string
}

// computeRiskScore mirrors the synthesizer's risk calculation
func computeRiskScore(findings []agentFinding) int {
	score := 0
	for _, f := range findings {
		score += severityWeight[f.Severity]
	}
	if score > 100 {
		return 100
	}
	return score
}

func TestAgentRiskScore_Empty(t *testing.T) {
	score := computeRiskScore(nil)
	if score != 0 {
		t.Errorf("no findings should give risk 0, got %d", score)
	}
}

func TestAgentRiskScore_SingleCritical(t *testing.T) {
	findings := []agentFinding{
		{Severity: "critical", Category: "security"},
	}
	score := computeRiskScore(findings)
	if score != 25 {
		t.Errorf("one critical should give risk 25, got %d", score)
	}
}

func TestAgentRiskScore_Mixed(t *testing.T) {
	findings := []agentFinding{
		{Severity: "critical"}, // 25
		{Severity: "high"},     // 15
		{Severity: "medium"},   // 8
		{Severity: "low"},      // 3
		{Severity: "info"},     // 1
	}
	// Total: 25 + 15 + 8 + 3 + 1 = 52
	score := computeRiskScore(findings)
	if score != 52 {
		t.Errorf("mixed findings should give risk 52, got %d", score)
	}
}

func TestAgentRiskScore_CappedAt100(t *testing.T) {
	// 5 critical findings = 5 * 25 = 125 -> capped to 100
	var findings []agentFinding
	for i := 0; i < 5; i++ {
		findings = append(findings, agentFinding{Severity: "critical"})
	}
	score := computeRiskScore(findings)
	if score != 100 {
		t.Errorf("should cap at 100, got %d", score)
	}
}

func TestAgentRiskScore_AllInfo(t *testing.T) {
	// 10 info findings = 10 * 1 = 10
	var findings []agentFinding
	for i := 0; i < 10; i++ {
		findings = append(findings, agentFinding{Severity: "info"})
	}
	score := computeRiskScore(findings)
	if score != 10 {
		t.Errorf("10 info should give risk 10, got %d", score)
	}
}

func TestAgentRecommendations_Dedup(t *testing.T) {
	// Simulate recommendation extraction with dedup by category:location
	findings := []agentFinding{
		{Severity: "critical", Category: "security", Location: "main:10", Suggestion: "fix A"},
		{Severity: "critical", Category: "security", Location: "main:10", Suggestion: "fix A again"},
		{Severity: "high", Category: "performance", Location: "engine:50", Suggestion: "fix B"},
		{Severity: "medium", Category: "correctness", Location: "book:30", Suggestion: "fix C"},
		{Severity: "low", Category: "performance", Location: "util:5", Suggestion: "fix D"},
	}

	// Extract top 3 unique recommendations
	var recs []string
	seen := map[string]bool{}
	for _, f := range findings {
		if len(recs) >= 3 {
			break
		}
		key := f.Category + ":" + f.Location
		if seen[key] {
			continue
		}
		seen[key] = true
		recs = append(recs, f.Suggestion)
	}

	if len(recs) != 3 {
		t.Errorf("expected 3 recommendations, got %d", len(recs))
	}
	if recs[0] != "fix A" {
		t.Errorf("first rec should be fix A, got %s", recs[0])
	}
	if recs[1] != "fix B" {
		t.Errorf("second rec should be fix B, got %s", recs[1])
	}
	if recs[2] != "fix C" {
		t.Errorf("third rec should be fix C, got %s", recs[2])
	}
}

func TestAgentStrengths(t *testing.T) {
	// If no security findings, security strength should be listed
	secCount := 0
	perfCount := 2
	corrCount := 0

	var strengths []string
	if secCount == 0 {
		strengths = append(strengths, "No security vulnerabilities detected")
	}
	if perfCount == 0 {
		strengths = append(strengths, "No performance bottlenecks detected")
	}
	if corrCount == 0 {
		strengths = append(strengths, "Matching engine invariants appear correct")
	}

	if len(strengths) != 2 {
		t.Errorf("expected 2 strengths (sec + corr clean), got %d", len(strengths))
	}
}

// Test the report score breakdown calculation
func TestReportScoreBreakdown(t *testing.T) {
	// Mirrors report.expDecay and report.sat
	p99 := 5_000_000.0    // 5ms
	tps := 50_000.0        // 50k/sec
	errPct := 2.0          // 2%

	speedScore := 100.0 * mathExpDecay(p99, 200_000_000)
	tputScore := 100.0 * mathSat(tps, 200_000)
	corrScore := 100.0 * (1 - errPct/100)
	composite := 0.4*speedScore + 0.4*tputScore + 0.2*corrScore

	if speedScore < 90 || speedScore > 100 {
		t.Errorf("5ms p99 should give speed ~97, got %f", speedScore)
	}
	if tputScore < 24 || tputScore > 26 {
		t.Errorf("50k tps should give throughput 25, got %f", tputScore)
	}
	if corrScore < 97 || corrScore > 99 {
		t.Errorf("2%% errors should give correctness 98, got %f", corrScore)
	}
	if composite < 55 || composite > 75 {
		t.Errorf("composite should be in 55-75 range, got %f", composite)
	}
}
