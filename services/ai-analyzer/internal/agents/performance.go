// Performance agent analyzes submission source code for latency hotspots,
// algorithmic complexity issues, and allocation patterns.
package agents

import (
	"context"

	"github.com/iicpc/ai-analyzer/internal/llm"
)

const performancePrompt = `You are a performance engineer reviewing a matching engine (order book) implementation.
This code will be stress-tested with thousands of concurrent orders per second.
Latency is measured at p50, p90, and p99 percentiles. Every microsecond matters.

Analyze the code for:
1. Algorithmic complexity: O(n) scans where O(1) or O(log n) is possible
2. Lock contention: mutexes held during I/O, broad lock scopes
3. Memory allocation: per-request allocations, slice growth in hot paths
4. Data structure choice: linked lists vs arrays, map vs sorted tree for order book
5. Serialization overhead: JSON in hot path instead of binary protocols
6. Goroutine leaks: unbounded goroutine spawning without lifecycle management
7. System call overhead: excessive syscalls (e.g., time.Now() per order)
8. Cache locality: pointer-heavy structures causing cache misses

For each finding, provide:
- severity: critical, high, medium, low, or info
- category: always "performance"
- location: the function or line where the issue exists
- description: what the performance problem is, with Big-O analysis if relevant
- suggestion: specific refactoring to improve it, with expected impact

Return your analysis as a JSON object with a "findings" array.
If the code is clean, return an empty findings array.
Be precise. Do not hallucinate issues that do not exist in the code.
IMPORTANT: You MUST respond with ONLY raw JSON. Do NOT include any markdown formatting, explanations, or conversational text. If the input source code is invalid, garbage, or missing, simply return an empty findings array.`

// RunPerformance analyzes code for algorithmic and mechanical inefficiencies.
func RunPerformance(ctx context.Context, provider llm.Provider, sourceCode string, logs string) ([]Finding, error) {
	promptText := "Source Code:\n" + sourceCode
	if logs != "" {
		promptText += "\n\nRuntime Logs (from Sandbox Execution):\n" + logs
	}

	req := &llm.GenerateRequest{
		SystemInstruct: &llm.Content{
			Parts: []llm.Part{{Text: performancePrompt}},
		},
		Contents: []llm.Content{
			{Role: "user", Parts: []llm.Part{{Text: promptText}}},
		},
		GenerationConfig: &llm.GenerationConfig{
			ResponseMimeType: "application/json",
			ResponseSchema:   FindingsSchema,
			Temperature:      0.1,
			MaxOutputTokens:  4096,
		},
	}

	var result struct {
		Findings []Finding `json:"findings"`
	}
	if err := llm.GenerateJSON(ctx, provider, req, &result); err != nil {
		return nil, err
	}

	for i := range result.Findings {
		result.Findings[i].Category = "performance"
	}
	return result.Findings, nil
}
