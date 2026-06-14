package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	ResponseSchema   *Schema     `json:"responseSchema,omitempty"`
	Temperature      float64     `json:"temperature,omitempty"`
	MaxOutputTokens  int         `json:"maxOutputTokens,omitempty"`
}

type Schema struct {
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]Schema `json:"properties,omitempty"`
	Items       *Schema           `json:"items,omitempty"`
	Required    []string          `json:"required,omitempty"`
}

// GenerateJSON calls Generate and parses the response as JSON into dst.
// Requires GenerationConfig.ResponseMimeType = "application/json".
func GenerateJSON(ctx context.Context, p Provider, req *GenerateRequest, dst interface{}) error {
	var text string
	var err error

	// Retry loop for transient 503/429 API errors
	for i := 0; i < 3; i++ {
		text, err = p.Generate(ctx, req)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "502") {
			time.Sleep(time.Duration(i+1) * 2 * time.Second)
			continue
		}
		break // Not a retriable error
	}

	if err != nil {
		return err
	}
	
	// Strip markdown and conversational filler by jumping to the first JSON bracket
	// and letting json.Decoder ignore any trailing garbage (like closing markdown ticks).
	start := strings.IndexAny(text, "{[")
	if start == -1 {
		return fmt.Errorf("no json structure found in response (raw: %.200s)", text)
	}

	if err := json.NewDecoder(strings.NewReader(text[start:])).Decode(dst); err != nil {
		return fmt.Errorf("parse json response: %w (raw: %.200s)", err, text)
	}
	return nil
}
