package jsonl

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/alfranz/rig/internal/llm"
)

// Total is the sum over a group of API calls.
type Total struct {
	Key     string // a day ("2026-10-18") or a model id
	Calls   int
	In, Out int
	Cost    float64
}

// Usage is the cost of all logged calls, grouped two ways.
type Usage struct {
	Days   []Total // newest day first
	Models []Total // most expensive first
	All    Total
	Files  int
}

// Summarize reads every *.jsonl file in dir. It only looks at "call" records.
func Summarize(dir string) (Usage, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Files: len(files)}
	days, models := map[string]*Total{}, map[string]*Total{}
	for _, f := range files {
		if err := readCalls(f, func(started time.Time, model string, us *llm.Usage) {
			day := started.Local().Format("2006-01-02")
			for _, t := range []*Total{group(days, day), group(models, model), &u.All} {
				t.Calls++
				if us != nil {
					t.In += us.PromptTokens
					t.Out += us.CompletionTokens
					t.Cost += us.Cost
				}
			}
		}); err != nil {
			return u, err
		}
	}
	u.Days = sorted(days, func(a, b Total) int { return cmp.Compare(b.Key, a.Key) })
	u.Models = sorted(models, func(a, b Total) int { return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(a.Key, b.Key)) })
	return u, nil
}

// readCalls decodes one record at a time, so long records with many raw lines need no line buffer.
func readCalls(path string, fn func(started time.Time, model string, u *llm.Usage)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var r struct {
			Type    string          `json:"type"`
			Started time.Time       `json:"started"`
			Model   string          `json:"model"`
			Request json.RawMessage `json:"request"`
			Usage   *llm.Usage      `json:"usage"`
		}
		if err := dec.Decode(&r); err != nil {
			// io.EOF, or a half-written last record from a session that is still running.
			return nil
		}
		if r.Type != "call" {
			continue
		}
		model := r.Model
		if model == "" {
			// A failed call has no response model. Use the one we asked for.
			var req struct {
				Model string `json:"model"`
			}
			json.Unmarshal(r.Request, &req)
			model = cmp.Or(req.Model, "(unknown)")
		}
		fn(r.Started, model, r.Usage)
	}
}

func group(m map[string]*Total, key string) *Total {
	t := m[key]
	if t == nil {
		t = &Total{Key: key}
		m[key] = t
	}
	return t
}

func sorted(m map[string]*Total, less func(a, b Total) int) []Total {
	out := make([]Total, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	slices.SortFunc(out, less)
	return out
}
