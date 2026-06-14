package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iicpc/ai-analyzer/internal/llm"
)

// LeaderboardRun represents a single engine's performance on the leaderboard.
type LeaderboardRun struct {
	Team       string  `json:"team"`
	Score      int     `json:"score"`
	P99        float64 `json:"p99_ns"`
	TPS        float64 `json:"tps"`
	ErrorRate  float64 `json:"error_rate"`
	SourceCode string  `json:"source_code,omitempty"` // Only populated for top 3 and bottom 3
}

// MetaAnalysisReport is the structured output from the Meta AI Agent.
type MetaAnalysisReport struct {
	GlobalSummary   string   `json:"global_summary"`
	WinningPatterns []string `json:"winning_patterns"`
	LosingPatterns  []string `json:"losing_patterns"`
	Recommendation  string   `json:"recommendation"`
}

const metaPrompt = `You are the Head Judge for a high-frequency trading matching engine competition.
You are given the current leaderboard standings.
Crucially, you are provided with the exact source code for the TOP 3 engines (the fastest) and the BOTTOM 3 engines (the slowest/most error-prone).

Your job is to analyze this data and generate a global meta-analysis report.

Compare the architecture, data structures, and algorithms of the winning engines against the losing engines.
Identify EXACTLY why the top engines are faster. Look for differences in memory allocation, locking strategies (mutexes vs lock-free), I/O handling, and data structures.

Output Format: Return ONLY a valid, parseable JSON object matching the exact structure above.
IMPORTANT: You MUST respond with ONLY raw JSON. Do NOT include any markdown formatting, explanations, or conversational text. If the input data is missing or garbage, simply return the structure with empty strings.

- "global_summary": A 2-3 sentence overview of the current state of the competition.
- "winning_patterns": Array of specific, highly technical patterns observed in the top 3 engines.
- "losing_patterns": Array of specific bottlenecks or anti-patterns observed in the bottom 3 engines.
- "recommendation": A final 1-2 sentence recommendation for competitors trying to improve their engines.

Be brutally honest, highly technical, and specific.`

// AnalyzeLeaderboard runs the meta-agent to synthesize a global leaderboard report.
func AnalyzeLeaderboard(ctx context.Context, provider llm.Provider, runs []LeaderboardRun) (*MetaAnalysisReport, error) {
	runsJSON, _ := json.MarshalIndent(runs, "", "  ")

	userContent := fmt.Sprintf("## LEADERBOARD AND SOURCE CODE CONTEXT\n```json\n%s\n```", string(runsJSON))

	req := &llm.GenerateRequest{
		SystemInstruct: &llm.Content{
			Parts: []llm.Part{{Text: metaPrompt}},
		},
		Contents: []llm.Content{
			{Role: "user", Parts: []llm.Part{{Text: userContent}}},
		},
		GenerationConfig: &llm.GenerationConfig{
			ResponseMimeType: "application/json",
			Temperature:      0.2,
			MaxOutputTokens:  4096,
		},
	}

	var report MetaAnalysisReport
	if err := llm.GenerateJSON(ctx, provider, req, &report); err != nil {
		return nil, fmt.Errorf("generate meta report: %w", err)
	}

	return &report, nil
}
