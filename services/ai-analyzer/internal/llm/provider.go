package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// Provider abstracts the underlying LLM API.
type Provider interface {
	Generate(ctx context.Context, req *GenerateRequest) (string, error)
}

// GenerateRequest is the unified payload (modelled after Gemini API).
type GenerateRequest struct {
	Contents         []Content         `json:"contents"`
	SystemInstruct   *Content          `json:"systemInstruction,omitempty"`
	GenerationConfig *GenerationConfig `json:"generationConfig,omitempty"`
}

type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text string `json:"text"`
}

type GenerationConfig struct {
	ResponseMimeType string      `json:"responseMimeType,omitempty"`
	ResponseSchema   interface{} `json:"responseSchema,omitempty"`
	Temperature      float64     `json:"temperature,omitempty"`
	MaxOutputTokens  int         `json:"maxOutputTokens,omitempty"`
}

// GenerateJSON calls Generate and parses the response as JSON into dst.
// Requires GenerationConfig.ResponseMimeType = "application/json".
func GenerateJSON(ctx context.Context, p Provider, req *GenerateRequest, dst interface{}) error {
	text, err := p.Generate(ctx, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), dst); err != nil {
		return fmt.Errorf("parse json response: %w (raw: %.200s)", err, text)
	}
	return nil
}
