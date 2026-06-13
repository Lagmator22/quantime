// Package report generates natural-language performance explanations
// by correlating telemetry metrics with source code analysis.
package report

import (
	"context"
	"fmt"

	"github.com/iicpc/ai-analyzer/internal/agents"
	"github.com/iicpc/ai-analyzer/internal/gemini"
)

// Metrics holds the telemetry data from a completed stress test run.
type Metrics struct {
	P50Ns    float64 `json:"p50"`
	P90Ns    float64 `json:"p90"`
	P99Ns    float64 `json:"p99"`
	TPS      float64 `json:"tps"`
	ErrPct   float64 `json:"err_pct"`
	Duration float64 `json:"duration_sec"`
}

const reportPrompt = `You are a quantitative performance analyst.
You are given:
1. Source code of a matching engine
2. Telemetry metrics from a real stress test against this engine

Your job is to explain WHY the engine performed the way it did, correlating
specific code patterns with specific metric outcomes.

Write your analysis as a JSON object with these fields:
- "summary": A 2-3 sentence plain English overview of performance
- "bottlenecks": Array of findings, each with severity/category/location/description/suggestion
- "optimizations": Array of 3-5 specific code changes that would improve performance, ordered by expected impact

Be specific. Reference actual function names and line numbers from the code.
Explain the causal relationship between code and metrics.
For example: "Your p99 spiked because cancelOrder() does a linear scan (O(n))
through the order book. At 50,000 resting orders, this takes ~50us per cancel.
Under burst load, these serialize and cause head-of-line blocking."

Do not hallucinate. Only reference code patterns that actually exist.`

// GeneratePerformanceReport creates a post-run analysis correlating
// telemetry data with source code patterns.
func GeneratePerformanceReport(ctx context.Context, client *gemini.Client, sourceCode string, metrics Metrics) (*agents.PerformanceReport, error) {
	metricsText := fmt.Sprintf(`Stress Test Results:
- p50 latency: %.0f ns (%.2f ms)
- p90 latency: %.0f ns (%.2f ms)
- p99 latency: %.0f ns (%.2f ms)
- Throughput: %.0f orders/sec
- Error rate: %.2f%%
- Test duration: %.1f seconds`,
		metrics.P50Ns, metrics.P50Ns/1e6,
		metrics.P90Ns, metrics.P90Ns/1e6,
		metrics.P99Ns, metrics.P99Ns/1e6,
		metrics.TPS,
		metrics.ErrPct,
		metrics.Duration,
	)

	userContent := fmt.Sprintf("SOURCE CODE:\n```\n%s\n```\n\nTELEMETRY DATA:\n%s", sourceCode, metricsText)

	req := &gemini.GenerateRequest{
		SystemInstruct: &gemini.Content{
			Parts: []gemini.Part{{Text: reportPrompt}},
		},
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{{Text: userContent}}},
		},
		GenerationConfig: &gemini.GenerationConfig{
			ResponseMimeType: "application/json",
			Temperature:      0.2, // Slightly higher for natural language
			MaxOutputTokens:  4096,
		},
	}

	var perfReport agents.PerformanceReport
	if err := client.GenerateJSON(ctx, req, &perfReport); err != nil {
		return nil, fmt.Errorf("generate performance report: %w", err)
	}

	// Compute score breakdown using the same formula as telemetry service
	perfReport.ScoreBreakdown.SpeedScore = 100.0 * expDecay(metrics.P99Ns, 200_000_000)
	perfReport.ScoreBreakdown.ThroughputScore = 100.0 * sat(metrics.TPS, 200_000)
	perfReport.ScoreBreakdown.CorrectnessScore = 100.0 * (1 - metrics.ErrPct/100)
	perfReport.ScoreBreakdown.CompositeScore = 0.4*perfReport.ScoreBreakdown.SpeedScore +
		0.4*perfReport.ScoreBreakdown.ThroughputScore +
		0.2*perfReport.ScoreBreakdown.CorrectnessScore

	return &perfReport, nil
}

// expDecay mirrors telemetry's scoring: 1 / (1 + x/k)
func expDecay(x, k float64) float64 {
	if x <= 0 {
		return 1
	}
	return 1.0 / (1.0 + x/k)
}

// sat mirrors telemetry's scoring: min(x/k, 1)
func sat(x, k float64) float64 {
	v := x / k
	if v > 1 {
		return 1
	}
	return v
}
