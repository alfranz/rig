package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const DefaultBaseURL = "https://openrouter.ai/api/v1"

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	// No overall timeout: a stream can run for minutes. Cancel with the context instead.
	return &Client{BaseURL: DefaultBaseURL, APIKey: apiKey, HTTP: &http.Client{}}
}

// Hooks let the caller watch a call while it runs. All fields are optional.
type Hooks struct {
	OnRequest   func(body []byte)                   // the exact JSON we send
	OnLine      func(line string, at time.Duration) // every raw line of the response body
	OnText      func(text string)                   // new assistant text
	OnReasoning func(text string)                   // new reasoning text
}

// Stream sends one streaming request and reads the Server-Sent Events (SSE) until the end.
// It returns a Result even on error, so the caller can still show the status and timings.
func (c *Client) Stream(ctx context.Context, req Request, h Hooks) (*Result, error) {
	req.Stream = true
	req.Usage = &UsageOption{Include: true}

	// json.Marshal would escape <, > and & as \u003c etc. Valid, but hard to read in the inspector.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	if h.OnRequest != nil {
		h.OnRequest(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	// Optional: names the app on the OpenRouter activity page.
	httpReq.Header.Set("X-Title", "rig")

	res := &Result{Started: time.Now()}
	defer func() { res.Duration = time.Since(res.Started) }()

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		// Errors before the stream starts come back as one normal JSON body.
		b, _ := io.ReadAll(resp.Body)
		if h.OnLine != nil {
			h.OnLine(string(b), time.Since(res.Started))
		}
		return res, fmt.Errorf("openrouter: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var acc accumulator
	// bufio.Reader and not bufio.Scanner: Scanner fails on lines longer than 64 KB.
	r := bufio.NewReader(resp.Body)
	for {
		line, readErr := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			if h.OnLine != nil {
				h.OnLine(line, time.Since(res.Started))
			}
			done, err := acc.handleLine(line, res, h)
			if err != nil {
				acc.finish(res)
				return res, err
			}
			if done {
				break
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			acc.finish(res)
			return res, fmt.Errorf("read stream: %w", readErr)
		}
	}
	acc.finish(res)
	return res, nil
}

// accumulator builds the assistant message from the stream chunks.
type accumulator struct {
	text      strings.Builder
	reasoning strings.Builder
	calls     map[int]*ToolCall // by tool call index
	gotFirst  bool
}

// handleLine handles one non-empty SSE line. It returns done=true on "data: [DONE]".
func (a *accumulator) handleLine(line string, res *Result, h Hooks) (bool, error) {
	// Lines that start with ":" are SSE comments. OpenRouter sends ": OPENROUTER PROCESSING" as a keep-alive.
	if strings.HasPrefix(line, ":") {
		return false, nil
	}
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return false, nil // "event:", "id:" and other SSE fields: not used by OpenRouter
	}
	data = strings.TrimSpace(data)
	if data == "[DONE]" {
		return true, nil
	}

	var ch Chunk
	if err := json.Unmarshal([]byte(data), &ch); err != nil {
		return false, fmt.Errorf("decode chunk: %w", err)
	}
	if ch.Error != nil {
		// An error after the stream has started arrives as a chunk, with HTTP status still 200.
		return false, fmt.Errorf("openrouter stream error %v: %s", ch.Error.Code, ch.Error.Message)
	}
	if ch.ID != "" {
		res.ID = ch.ID
	}
	if ch.Model != "" {
		res.Model = ch.Model
	}
	if ch.Provider != "" {
		res.Provider = ch.Provider
	}
	if ch.Usage != nil {
		res.Usage = ch.Usage
	}

	for _, choice := range ch.Choices {
		d := choice.Delta
		if !a.gotFirst && (d.Content != "" || d.Reasoning != "" || len(d.ToolCalls) > 0) {
			a.gotFirst = true
			res.TTFT = time.Since(res.Started)
		}
		if d.Content != "" {
			a.text.WriteString(d.Content)
			if h.OnText != nil {
				h.OnText(d.Content)
			}
		}
		if d.Reasoning != "" {
			a.reasoning.WriteString(d.Reasoning)
			if h.OnReasoning != nil {
				h.OnReasoning(d.Reasoning)
			}
		}
		for _, tc := range d.ToolCalls {
			if a.calls == nil {
				a.calls = map[int]*ToolCall{}
			}
			call, ok := a.calls[tc.Index]
			if !ok {
				call = &ToolCall{Type: "function"}
				a.calls[tc.Index] = call
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			call.Function.Arguments += tc.Function.Arguments
		}
		if choice.FinishReason != nil {
			res.FinishReason = *choice.FinishReason
		}
	}
	return false, nil
}

func (a *accumulator) finish(res *Result) {
	res.Message = Message{Role: "assistant", Content: a.text.String()}
	res.Reasoning = a.reasoning.String()
	indexes := make([]int, 0, len(a.calls))
	for i := range a.calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		res.Message.ToolCalls = append(res.Message.ToolCalls, *a.calls[i])
	}
}
