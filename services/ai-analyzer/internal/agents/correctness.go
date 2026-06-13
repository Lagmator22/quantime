// Correctness agent verifies matching engine invariants: price-time priority,
// order lifecycle, and numerical precision.
package agents

import (
	"context"

	"github.com/iicpc/ai-analyzer/internal/llm"
)

const correctnessPrompt = `You are a financial exchange compliance auditor reviewing a matching engine implementation.
A matching engine must maintain strict invariants to be considered correct.

Verify the code against these rules:
1. Price-time priority: orders at the same price must be filled in FIFO order
2. Best price execution: a buy order must match against the lowest available sell
3. Partial fills: remaining quantity must stay in the book at the original price level
4. Cancel correctness: cancelled orders must not appear in future matches
5. Self-trade prevention: orders from the same participant should not match (if applicable)
6. Numerical precision: floating-point prices must not cause rounding errors (prefer integers)
7. Order types: limit, market, IOC, FOK must each behave per exchange specification
8. Overflow protection: quantity * price must not overflow integer types
9. Negative price/quantity: must be rejected at input validation
10. Empty book behavior: market orders on an empty book should be rejected, not crash

For each finding, provide:
- severity: critical, high, medium, low, or info
- category: always "correctness"
- location: the function or line where the issue exists
- description: which invariant is violated and how
- suggestion: the correct behavior and code fix

Return your analysis as a JSON object with a "findings" array.
Be precise. Only report real violations visible in the code.`

// RunCorrectness checks matching engine invariant compliance.
func RunCorrectness(ctx context.Context, provider llm.Provider, sourceCode string) ([]Finding, error) {
	req := &llm.GenerateRequest{
		SystemInstruct: &llm.Content{
			Parts: []llm.Part{{Text: correctnessPrompt}},
		},
		Contents: []llm.Content{
			{Role: "user", Parts: []llm.Part{{Text: sourceCode}}},
		},
		GenerationConfig: &llm.GenerationConfig{
			ResponseMimeType: "application/json",
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
		result.Findings[i].Category = "correctness"
	}
	return result.Findings, nil
}
