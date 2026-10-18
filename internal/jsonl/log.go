// Package jsonl writes every API call and tool run to a JSON Lines file, one record per line.
// Read it with: jq . ~/.rig/logs/<session>.jsonl
package jsonl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alfranz/rig/internal/agent"
	"github.com/alfranz/rig/internal/llm"
)

type Logger struct {
	Path string

	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	calls map[int]*callRecord
}

type line struct {
	OffsetMS int64  `json:"offset_ms"`
	Line     string `json:"line"`
}

type callRecord struct {
	Type         string          `json:"type"` // "call"
	CallID       int             `json:"call_id"`
	Started      time.Time       `json:"started"`
	Request      json.RawMessage `json:"request"`
	Status       int             `json:"status"`
	GenerationID string          `json:"generation_id,omitempty"`
	Model        string          `json:"model,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Message      *llm.Message    `json:"message,omitempty"`
	Usage        *llm.Usage      `json:"usage,omitempty"`
	TTFTMS       int64           `json:"ttft_ms"`
	DurationMS   int64           `json:"duration_ms"`
	Error        string          `json:"error,omitempty"`
	Lines        []line          `json:"lines"`
}

type toolRecord struct {
	Type       string    `json:"type"` // "tool"
	Time       time.Time `json:"time"`
	ToolCallID string    `json:"tool_call_id"`
	Name       string    `json:"name"`
	Output     string    `json:"output"`
	IsError    bool      `json:"is_error"`
	DurationMS int64     `json:"duration_ms"`
}

// Open creates a new log file in dir, named after the current time.
func Open(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, time.Now().Format("2006-01-02T15-04-05")+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{Path: p, f: f, enc: json.NewEncoder(f), calls: map[int]*callRecord{}}, nil
}

// Handle collects the events of a call and writes one record when the call ends.
func (l *Logger) Handle(e agent.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch e := e.(type) {
	case agent.CallStarted:
		l.calls[e.CallID] = &callRecord{Type: "call", CallID: e.CallID, Started: e.At, Request: e.Request}
	case agent.StreamLine:
		if r := l.calls[e.CallID]; r != nil {
			r.Lines = append(r.Lines, line{OffsetMS: e.Offset.Milliseconds(), Line: e.Line})
		}
	case agent.CallFinished:
		r := l.calls[e.CallID]
		if r == nil {
			return
		}
		delete(l.calls, e.CallID)
		if res := e.Result; res != nil {
			r.Status = res.Status
			r.GenerationID = res.ID
			r.Model = res.Model
			r.Provider = res.Provider
			r.FinishReason = res.FinishReason
			r.Message = &res.Message
			r.Usage = res.Usage
			r.TTFTMS = res.TTFT.Milliseconds()
			r.DurationMS = res.Duration.Milliseconds()
		}
		if e.Err != nil {
			r.Error = e.Err.Error()
		}
		l.enc.Encode(r)
	case agent.Cleared:
		l.enc.Encode(struct {
			Type string    `json:"type"` // "cleared"
			Time time.Time `json:"time"`
		}{"cleared", e.At})
	case agent.ModelChanged:
		l.enc.Encode(struct {
			Type string    `json:"type"` // "model"
			Time time.Time `json:"time"`
			From string    `json:"from"`
			To   string    `json:"to"`
		}{"model", e.At, e.From, e.To})
	case agent.ToolFinished:
		l.enc.Encode(toolRecord{
			Type: "tool", Time: time.Now(), ToolCallID: e.ToolCallID, Name: e.Name,
			Output: e.Output, IsError: e.IsError, DurationMS: e.Duration.Milliseconds(),
		})
	}
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
