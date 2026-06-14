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

type GeminiProvider struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func NewGeminiProvider(apiKey, model string) *GeminiProvider {
	if model == "" {
		model = "gemini-3.5-flash"
	}
	// Native Gemini API URL
	baseURL := "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent"
	return &GeminiProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 500 * time.Second,
		},
	}
}

func (c *GeminiProvider) Generate(ctx context.Context, req *GenerateRequest) (string, error) {
	// The internal GenerateRequest maps 1:1 to the Gemini API JSON schema.
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL
	if c.apiKey != "" {
		url += "?key=" + c.apiKey
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

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

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if geminiResp.Error != nil {
		return "", fmt.Errorf("gemini error: %s", geminiResp.Error.Message)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from gemini")
	}

	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}
