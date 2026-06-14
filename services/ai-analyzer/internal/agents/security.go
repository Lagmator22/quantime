// Security agent analyzes submission source code for sandbox escape vectors,
// memory safety issues, and syscall abuse patterns.
package agents

import (
	"context"

	"github.com/iicpc/ai-analyzer/internal/llm"
)

const securityPrompt = `You are a security auditor for a competitive programming platform.
You are reviewing source code for a matching engine (stock exchange) that will run inside a Docker container with these constraints:
- Read-only rootfs
- No capabilities (all dropped)
- no-new-privileges security option
- Memory limited to 256MB
- PID limit of 128
- Network restricted to an internal bridge

Analyze the code for:
1. Container escape attempts (writing to /proc, /sys, mounting filesystems)
2. Resource exhaustion (fork bombs, memory leaks, goroutine leaks)
3. Unsafe memory operations (buffer overflows, use-after-free in C/C++)
4. Network abuse (port scanning, DNS exfiltration)
5. Filesystem abuse (symlink attacks, /tmp exhaustion)
6. Input validation (integer overflow on prices/quantities, NaN/Infinity handling)

For each finding, provide:
- severity: critical, high, medium, low, or info
- category: always "security"
- location: the function or line where the issue exists
- description: what the vulnerability is
- suggestion: specific code to fix it

Return your analysis as a JSON object with a "findings" array.
If the code is clean, return an empty findings array.
Be precise. Do not hallucinate issues that do not exist in the code.
IMPORTANT: You MUST respond with ONLY raw JSON. Do NOT include any markdown formatting, explanations, or conversational text. If the input source code is invalid, garbage, or missing, simply return an empty findings array.`

// RunSecurity analyzes code for security vulnerabilities.
func RunSecurity(ctx context.Context, provider llm.Provider, sourceCode string, logs string) ([]Finding, error) {
	promptText := "Source Code:\n" + sourceCode
	if logs != "" {
		promptText += "\n\nRuntime Logs (from Sandbox Execution):\n" + logs
	}

	req := &llm.GenerateRequest{
		SystemInstruct: &llm.Content{
			Parts: []llm.Part{{Text: securityPrompt}},
		},
		Contents: []llm.Content{
			{Role: "user", Parts: []llm.Part{{Text: promptText}}},
		},
		GenerationConfig: &llm.GenerationConfig{
			ResponseMimeType: "application/json",
			ResponseSchema:   FindingsSchema,
			Temperature:      0.1, // Low temp for precise analysis
			MaxOutputTokens:  4096,
		},
	}

	var result struct {
		Findings []Finding `json:"findings"`
	}
	if err := llm.GenerateJSON(ctx, provider, req, &result); err != nil {
		return nil, err
	}

	// Tag all findings with security category
	for i := range result.Findings {
		result.Findings[i].Category = "security"
	}
	return result.Findings, nil
}
