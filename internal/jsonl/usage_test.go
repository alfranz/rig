package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alfranz/rig/internal/agent"
	"github.com/alfranz/rig/internal/llm"
)

func TestSummarize(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)
	call := func(id int, at time.Time, model string, cost float64) {
		l.Handle(agent.CallStarted{CallID: id, At: at, Request: []byte(`{"model":"asked/model"}`)})
		var res *llm.Result
		if model != "" {
			res = &llm.Result{Status: 200, Model: model, Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 10, Cost: cost}}
		}
		l.Handle(agent.CallFinished{CallID: id, Result: res})
	}
	call(1, day1, "a/cheap", 0.01)
	l.Handle(agent.ModelChanged{From: "a/cheap", To: "b/pricey", At: day1})
	call(2, day2, "b/pricey", 0.50)
	call(3, day2, "b/pricey", 0.25)
	call(4, day2, "", 0) // failed call: no result, model comes from the request
	l.Close()

	// A half-written record at the end must not break the summary.
	os.WriteFile(filepath.Join(dir, "partial.jsonl"), []byte(`{"type":"call","started":"2026-10-18T10:00:00Z","model":"x`), 0o600)

	u, err := Summarize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.Files != 2 || u.All.Calls != 4 || u.All.In != 300 || u.All.Cost < 0.7599 || u.All.Cost > 0.7601 {
		t.Errorf("all = %+v, files = %d", u.All, u.Files)
	}
	if len(u.Days) != 2 || u.Days[0].Key != "2026-10-18" || u.Days[0].Calls != 3 {
		t.Errorf("days = %+v", u.Days)
	}
	if len(u.Models) != 3 || u.Models[0].Key != "b/pricey" || u.Models[0].Calls != 2 || u.Models[2].Key != "asked/model" {
		t.Errorf("models = %+v", u.Models)
	}
}
