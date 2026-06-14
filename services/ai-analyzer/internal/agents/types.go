// Package agents defines the multi-agent code analysis pipeline.
// Each agent is a specialized system prompt that analyzes source code
// for a specific concern (security, performance, correctness).
// The synthesizer combines all findings into a unified report.
package agents

import (
	"github.com/iicpc/ai-analyzer/internal/llm"
)

// Finding represents a single issue found by an agent.
type Finding struct {
	Severity    string `json:"severity"`    // critical, high, medium, low, info
	Category    string `json:"category"`    // security, performance, correctness
	Location    string `json:"location"`    // file:line or function name
	Description string `json:"description"` // what the issue is
	Suggestion  string `json:"suggestion"`  // how to fix it
}

// AnalysisReport is the combined output from all agents.
type AnalysisReport struct {
	Findings       []Finding `json:"findings"`
	RiskScore      int       `json:"riskScore"`      // 0 (safe) to 100 (dangerous)
	Summary        string    `json:"summary"`        // one paragraph overview
	Strengths      []string  `json:"strengths"`      // what the code does well
	Recommendations []string `json:"recommendations"` // top 3 things to fix first
}

// PerformanceReport is generated after a stress test completes.
// Correlates telemetry metrics with source code to explain bottlenecks.
type PerformanceReport struct {
	Summary        string   `json:"summary"`        // plain English overview
	Bottlenecks    []Finding `json:"bottlenecks"`    // performance hotspots found
	Optimizations  []string `json:"optimizations"`  // suggested code changes
	ScoreBreakdown struct {
		SpeedScore       float64 `json:"speedScore"`
		ThroughputScore  float64 `json:"throughputScore"`
		CorrectnessScore float64 `json:"correctnessScore"`
		CompositeScore   float64 `json:"compositeScore"`
	} `json:"scoreBreakdown"`
}

var FindingsSchema = &llm.Schema{
	Type: "object",
	Properties: map[string]llm.Schema{
		"findings": {
			Type: "array",
			Items: &llm.Schema{
				Type: "object",
				Properties: map[string]llm.Schema{
					"severity":    {Type: "string"},
					"category":    {Type: "string"},
					"location":    {Type: "string"},
					"description": {Type: "string"},
					"suggestion":  {Type: "string"},
				},
				Required: []string{"severity", "category", "location", "description", "suggestion"},
			},
		},
	},
	Required: []string{"findings"},
}

var SynthesizerSchema = &llm.Schema{
	Type: "object",
	Properties: map[string]llm.Schema{
		"summary": {Type: "string"},
		"strengths": {
			Type: "array",
			Items: &llm.Schema{Type: "string"},
		},
		"recommendations": {
			Type: "array",
			Items: &llm.Schema{Type: "string"},
		},
	},
	Required: []string{"summary", "strengths", "recommendations"},
}
