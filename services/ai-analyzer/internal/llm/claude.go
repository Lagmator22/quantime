package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type ClaudeProvider struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func NewClaudeProvider(apiKey, model string) *ClaudeProvider {
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	return &ClaudeProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com/v1/messages",
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *ClaudeProvider) Generate(ctx context.Context, req *GenerateRequest) (string, error) {
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var messages []Message
	var system string

	if req.SystemInstruct != nil && len(req.SystemInstruct.Parts) > 0 {
		system = req.SystemInstruct.Parts[0].Text
	}
	for _, c := range req.Contents {
		role := c.Role
		if role == "" || role == "model" {
			// Anthropic uses 'assistant' instead of 'model'
			if role == "model" {
				role = "assistant"
			} else {
				role = "user"
			}
		}
		if len(c.Parts) > 0 {
			messages = append(messages, Message{
				Role:    role,
				Content: c.Parts[0].Text,
			})
		}
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": 4096, // required by Anthropic
	}
	if system != "" {
		payload["system"] = system
	}

	if req.GenerationConfig != nil {
		payload["temperature"] = req.GenerationConfig.Temperature
		if req.GenerationConfig.MaxOutputTokens > 0 {
			payload["max_tokens"] = req.GenerationConfig.MaxOutputTokens
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
	}

	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &claudeResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if claudeResp.Error != nil {
		return "", fmt.Errorf("claude error: %s", claudeResp.Error.Message)
	}

	if len(claudeResp.Content) == 0 {
		return "", fmt.Errorf("empty response from claude")
	}

	return claudeResp.Content[0].Text, nil
}
