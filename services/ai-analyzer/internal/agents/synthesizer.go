// Synthesizer combines findings from all agents into a unified report.
// Runs all agents concurrently, then merges and deduplicates results.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/iicpc/ai-analyzer/internal/llm"
)

// severityWeight maps severity to a numeric weight for risk score calculation.
var severityWeight = map[string]int{
	"critical": 25,
	"high":     15,
	"medium":   8,
	"low":      3,
	"info":     1,
}

// Analyze runs all three agents concurrently and synthesizes results.
// Returns a complete AnalysisReport with risk score and recommendations.
func Analyze(ctx context.Context, provider llm.Provider, sourceCode string) (*AnalysisReport, error) {
	var (
		secFindings  []Finding
		perfFindings []Finding
		corrFindings []Finding
		secErr       error
		perfErr      error
		corrErr      error
		wg           sync.WaitGroup
	)

	// Run all agents concurrently for faster analysis
	wg.Add(3)
	go func() {
		defer wg.Done()
		secFindings, secErr = RunSecurity(ctx, provider, sourceCode)
	}()
	go func() {
		defer wg.Done()
		perfFindings, perfErr = RunPerformance(ctx, provider, sourceCode)
	}()
	go func() {
		defer wg.Done()
		corrFindings, corrErr = RunCorrectness(ctx, provider, sourceCode)
	}()
	wg.Wait()

	// Collect all findings, noting agent failures as info-level findings
	var allFindings []Finding

	if secErr != nil {
		allFindings = append(allFindings, Finding{
			Severity:    "info",
			Category:    "security",
			Location:    "agent",
			Description: fmt.Sprintf("Security agent error: %v", secErr),
			Suggestion:  "Retry analysis or check API key configuration",
		})
	} else {
		allFindings = append(allFindings, secFindings...)
	}

	if perfErr != nil {
		allFindings = append(allFindings, Finding{
			Severity:    "info",
			Category:    "performance",
			Location:    "agent",
			Description: fmt.Sprintf("Performance agent error: %v", perfErr),
			Suggestion:  "Retry analysis or check API key configuration",
		})
	} else {
		allFindings = append(allFindings, perfFindings...)
	}

	if corrErr != nil {
		allFindings = append(allFindings, Finding{
			Severity:    "info",
			Category:    "correctness",
			Location:    "agent",
			Description: fmt.Sprintf("Correctness agent error: %v", corrErr),
			Suggestion:  "Retry analysis or check API key configuration",
		})
	} else {
		allFindings = append(allFindings, corrFindings...)
	}

	// Sort findings by severity (critical first)
	sort.Slice(allFindings, func(i, j int) bool {
		wi := severityWeight[allFindings[i].Severity]
		wj := severityWeight[allFindings[j].Severity]
		return wi > wj // Descending order
	})

	// Generate the summary and risk score
	riskScore := 0
	for _, f := range allFindings {
		riskScore += severityWeight[f.Severity]
	}
	if riskScore > 100 {
		riskScore = 100
	}

	// Now ask LLM to synthesize a summary paragraph and top recommendations
	findingsJSON, _ := json.Marshal(allFindings)
	prompt := fmt.Sprintf(synthesizerPrompt, string(findingsJSON))

	req := &llm.GenerateRequest{
		Contents: []llm.Content{
			{Role: "user", Parts: []llm.Part{{Text: prompt}}},
		},
		GenerationConfig: &llm.GenerationConfig{
			ResponseMimeType: "application/json",
			Temperature:      0.2, // Low temp for factual synthesis
			MaxOutputTokens:  1024,
		},
	}

	var synthesis struct {
		Summary         string   `json:"summary"`
		Strengths       []string `json:"strengths"`
		Recommendations []string `json:"recommendations"`
	}

	// If the synthesizer LLM fails, we still return the raw findings
	if err := llm.GenerateJSON(ctx, provider, req, &synthesis); err != nil {
		synthesis.Summary = "Failed to generate AI summary: " + err.Error()
	}

	return &AnalysisReport{
		Findings:        allFindings,
		RiskScore:       riskScore,
		Summary:         synthesis.Summary,
		Strengths:       synthesis.Strengths,
		Recommendations: synthesis.Recommendations,
	}, nil
}

const synthesizerPrompt = `You are the lead engineer reviewing a matching engine.
Below is a JSON array of findings from the security, performance, and correctness automated agents.

Your job is to synthesize these findings into three fields:
1. "summary": A single, concise, professional paragraph summarizing the overall quality and the most glaring issues.
2. "strengths": An array of 1-3 strings noting what the code does well (e.g., "No memory leaks", "Fast integer arithmetic"). If none, leave empty.
3. "recommendations": An array of 1-3 strings noting the most critical things the developer should fix first.

Return ONLY a JSON object matching this schema. Do not include markdown formatting or explanation.

Findings JSON:
%s`
