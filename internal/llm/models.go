package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// ModelInfo is one entry of GET /models. We keep only the fields the picker shows.
type ModelInfo struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	ContextLength       int      `json:"context_length"`
	Pricing             Pricing  `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
	TopProvider         struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Reasoning *struct {
		SupportedEfforts []string `json:"supported_efforts"`
		DefaultEffort    string   `json:"default_effort"`
	} `json:"reasoning"` // nil when the model does not reason
	Benchmarks struct {
		// Artificial Analysis indexes, 0 to 100. Nil when they have not measured the model.
		ArtificialAnalysis *struct {
			Intelligence *float64 `json:"intelligence_index"`
			Coding       *float64 `json:"coding_index"`
			Agentic      *float64 `json:"agentic_index"`
		} `json:"artificial_analysis"`
	} `json:"benchmarks"`
}

// SupportsTools reports if the model accepts the "tools" array. rig cannot work without it.
func (m ModelInfo) SupportsTools() bool { return slices.Contains(m.SupportedParameters, "tools") }

// Intelligence is the Artificial Analysis intelligence index, or nil.
func (m ModelInfo) Intelligence() *float64 {
	if aa := m.Benchmarks.ArtificialAnalysis; aa != nil {
		return aa.Intelligence
	}
	return nil
}

// Pricing is in US dollars per token. OpenRouter sends the numbers as strings, for example "0.000003".
type Pricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// PerMillion turns a per-token price string into dollars per million tokens. ok is false for a missing or bad value.
func PerMillion(price string) (v float64, ok bool) {
	f, err := strconv.ParseFloat(price, 64)
	if err != nil || f < 0 {
		return 0, false // router models like openrouter/auto send "-1"
	}
	return f * 1e6, true
}

// Models lists the models OpenRouter offers. The endpoint needs no API key, but we send it anyway.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openrouter models: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var body struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	return body.Data, nil
}
