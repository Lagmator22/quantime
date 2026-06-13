// Synthesizer combines findings from all agents into a unified report.
// Runs all agents concurrently, then merges and deduplicates results.
package agents

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/iicpc/ai-analyzer/internal/gemini"
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
func Analyze(ctx context.Context, client *gemini.Client, sourceCode string) (*AnalysisReport, error) {
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
		secFindings, secErr = RunSecurity(ctx, client, sourceCode)
	}()
	go func() {
		defer wg.Done()
		perfFindings, perfErr = RunPerformance(ctx, client, sourceCode)
	}()
	go func() {
		defer wg.Done()
		corrFindings, corrErr = RunCorrectness(ctx, client, sourceCode)
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
		return severityWeight[allFindings[i].Severity] > severityWeight[allFindings[j].Severity]
	})

	// Compute risk score: sum of severity weights, capped at 100
	riskScore := 0
	for _, f := range allFindings {
		riskScore += severityWeight[f.Severity]
	}
	if riskScore > 100 {
		riskScore = 100
	}

	// Build top recommendations from the highest severity findings
	var recommendations []string
	seen := map[string]bool{}
	for _, f := range allFindings {
		if len(recommendations) >= 3 {
			break
		}
		key := f.Category + ":" + f.Location
		if seen[key] {
			continue
		}
		seen[key] = true
		recommendations = append(recommendations, f.Suggestion)
	}

	// Identify strengths (areas with no findings)
	var strengths []string
	hasSec := len(secFindings) > 0 || secErr != nil
	hasPerf := len(perfFindings) > 0 || perfErr != nil
	hasCorr := len(corrFindings) > 0 || corrErr != nil
	if !hasSec {
		strengths = append(strengths, "No security vulnerabilities detected")
	}
	if !hasPerf {
		strengths = append(strengths, "No performance bottlenecks detected")
	}
	if !hasCorr {
		strengths = append(strengths, "Matching engine invariants appear correct")
	}

	// Generate summary
	summary := generateSummary(len(allFindings), riskScore, len(secFindings), len(perfFindings), len(corrFindings))

	return &AnalysisReport{
		Findings:        allFindings,
		RiskScore:       riskScore,
		Summary:         summary,
		Strengths:       strengths,
		Recommendations: recommendations,
	}, nil
}

// generateSummary creates a human-readable overview of the analysis.
func generateSummary(total, risk, sec, perf, corr int) string {
	if total == 0 {
		return "Analysis complete. No issues found. The code appears well-written and safe for deployment."
	}

	riskLevel := "low"
	if risk > 70 {
		riskLevel = "critical"
	} else if risk > 40 {
		riskLevel = "high"
	} else if risk > 20 {
		riskLevel = "moderate"
	}

	return fmt.Sprintf(
		"Analysis complete. Found %d issues (risk score: %d/100, level: %s). "+
			"Security: %d findings, Performance: %d findings, Correctness: %d findings. "+
			"Address critical and high severity items before stress testing.",
		total, risk, riskLevel, sec, perf, corr,
	)
}
