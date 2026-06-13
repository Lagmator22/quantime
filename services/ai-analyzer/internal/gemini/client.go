// Package gemini wraps the Google Gemini REST API for structured code analysis.
// Uses raw net/http to avoid external SDK dependencies and keep the binary small.
// Supports structured JSON output via response_mime_type.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client holds the API key and HTTP client for Gemini calls.
type Client struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewClient creates a Gemini client. Model defaults to gemini-2.5-flash.
func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &Client{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://generativelanguage.googleapis.com/v1beta",
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// GenerateRequest is the payload sent to Gemini generateContent endpoint.
type GenerateRequest struct {
	Contents         []Content         `json:"contents"`
	SystemInstruct   *Content          `json:"systemInstruction,omitempty"`
	GenerationConfig *GenerationConfig `json:"generationConfig,omitempty"`
}

// Content holds a single message part (user or model).
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part is a text chunk within a Content.
type Part struct {
	Text string `json:"text"`
}

// GenerationConfig controls output format and limits.
type GenerationConfig struct {
	ResponseMimeType string      `json:"responseMimeType,omitempty"`
	ResponseSchema   interface{} `json:"responseSchema,omitempty"`
	Temperature      float64     `json:"temperature,omitempty"`
	MaxOutputTokens  int         `json:"maxOutputTokens,omitempty"`
}

// GenerateResponse is the API response from Gemini.
type GenerateResponse struct {
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

// Generate calls the Gemini API and returns the text response.
// If the API returns an error, it is surfaced as a Go error.
func (c *Client) Generate(ctx context.Context, req *GenerateRequest) (string, error) {
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
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

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
	}

	var genResp GenerateResponse
	if err := json.Unmarshal(respBody, &genResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if genResp.Error != nil {
		return "", fmt.Errorf("gemini error %d: %s", genResp.Error.Code, genResp.Error.Message)
	}

	if len(genResp.Candidates) == 0 || len(genResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from gemini")
	}

	return genResp.Candidates[0].Content.Parts[0].Text, nil
}

// GenerateJSON calls Generate and parses the response as JSON into dst.
// Requires GenerationConfig.ResponseMimeType = "application/json".
func (c *Client) GenerateJSON(ctx context.Context, req *GenerateRequest, dst interface{}) error {
	text, err := c.Generate(ctx, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), dst); err != nil {
		return fmt.Errorf("parse json response: %w (raw: %.200s)", err, text)
	}
	return nil
}
