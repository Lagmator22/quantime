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

// OpenAIProvider works for OpenAI, DeepSeek, Kimi, and local OpenAI-compatible APIs.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1/chat/completions"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *OpenAIProvider) Generate(ctx context.Context, req *GenerateRequest) (string, error) {
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var messages []Message

	if req.SystemInstruct != nil && len(req.SystemInstruct.Parts) > 0 {
		messages = append(messages, Message{
			Role:    "system",
			Content: req.SystemInstruct.Parts[0].Text,
		})
	}
	for _, c := range req.Contents {
		role := c.Role
		if role == "" {
			role = "user"
		}
		if len(c.Parts) > 0 {
			messages = append(messages, Message{
				Role:    role,
				Content: c.Parts[0].Text,
			})
		}
	}

	payload := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
	}

	if req.GenerationConfig != nil {
		payload["temperature"] = req.GenerationConfig.Temperature
		if req.GenerationConfig.MaxOutputTokens > 0 {
			payload["max_tokens"] = req.GenerationConfig.MaxOutputTokens
		}
		if req.GenerationConfig.ResponseMimeType == "application/json" {
			payload["response_format"] = map[string]string{"type": "json_object"}
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
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

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

	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if openAIResp.Error != nil {
		return "", fmt.Errorf("llm error: %s", openAIResp.Error.Message)
	}

	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from llm")
	}

	return openAIResp.Choices[0].Message.Content, nil
}
