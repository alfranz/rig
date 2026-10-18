package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sseServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !req.Stream || req.Usage == nil || !req.Usage.Include {
			t.Errorf("stream=%v usage=%v, want both set", req.Stream, req.Usage)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("test-key")
	c.BaseURL = srv.URL
	return c
}

func TestStreamAssemblesTextAndToolCalls(t *testing.T) {
	body := strings.Join([]string{
		": OPENROUTER PROCESSING",
		"",
		`data: {"id":"gen-1","model":"m","provider":"P","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me "}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"look."}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}`,
		"",
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: {"id":"gen-1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"cost":0.0004}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	c := sseServer(t, 200, body)

	var lines []string
	var text strings.Builder
	res, err := c.Stream(context.Background(), Request{Model: "m"}, Hooks{
		OnLine: func(l string, _ time.Duration) { lines = append(lines, l) },
		OnText: func(s string) { text.WriteString(s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != "Let me look." || text.String() != "Let me look." {
		t.Errorf("content = %q, streamed = %q", res.Message.Content, text.String())
	}
	if res.FinishReason != "tool_calls" || res.ID != "gen-1" || res.Provider != "P" {
		t.Errorf("finish=%q id=%q provider=%q", res.FinishReason, res.ID, res.Provider)
	}
	if res.Usage == nil || res.Usage.PromptTokens != 100 || res.Usage.Cost != 0.0004 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if len(res.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", res.Message.ToolCalls)
	}
	if tc := res.Message.ToolCalls[0]; tc.ID != "call_a" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("call 0 = %+v", tc)
	}
	if tc := res.Message.ToolCalls[1]; tc.ID != "call_b" || tc.Function.Name != "list_files" {
		t.Errorf("call 1 = %+v", tc)
	}
	if len(lines) != 10 || lines[0] != ": OPENROUTER PROCESSING" {
		t.Errorf("got %d raw lines, first %q", len(lines), lines[0])
	}
}

func TestStreamHTTPError(t *testing.T) {
	c := sseServer(t, 401, `{"error":{"code":401,"message":"No auth credentials found"}}`)
	res, err := c.Stream(context.Background(), Request{Model: "m"}, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "No auth credentials") {
		t.Fatalf("err = %v", err)
	}
	if res.Status != 401 {
		t.Errorf("status = %d", res.Status)
	}
}

func TestStreamErrorChunk(t *testing.T) {
	body := `data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"error":{"code":502,"message":"provider died"},"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}` + "\n\n"
	c := sseServer(t, 200, body)
	res, err := c.Stream(context.Background(), Request{Model: "m"}, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "provider died") {
		t.Fatalf("err = %v", err)
	}
	if res.Message.Content != "hi" {
		t.Errorf("partial content = %q", res.Message.Content)
	}
}
