package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/tools"
)

func TestParseAndLoad(t *testing.T) {
	file := "---\nname: general\nmodel: a/b\ntools: [read_file]\n---\nYou are helpful.\n"
	builtin := fstest.MapFS{".rig/agents/general.md": {Data: []byte(file)}}

	a, err := Load("general", []string{t.TempDir()}, builtin)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "general" || a.Model != "a/b" || a.Prompt != "You are helpful." || a.Source != "built-in" || len(a.Tools) != 1 {
		t.Fatalf("agent = %+v", a)
	}

	// A file on disk wins over the built-in one.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "general.md"), []byte("---\nname: general\nmodel: c/d\n---\nlocal"), 0o644)
	if a, err = Load("general", []string{dir}, builtin); err != nil || a.Model != "c/d" {
		t.Fatalf("local agent = %+v, %v", a, err)
	}

	if _, err := Parse([]byte("no header"), "x"); err == nil {
		t.Fatal("want error for missing header")
	}
}

func TestProjectInstructionsFallback(t *testing.T) {
	a := &Agent{Prompt: "base"}
	dir := t.TempDir()
	if p := a.SystemPrompt(dir, time.Now()); strings.Contains(p, "project_instructions") {
		t.Fatalf("no files: prompt has instructions:\n%s", p)
	}

	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("from claude"), 0o644)
	if p := a.SystemPrompt(dir, time.Now()); !strings.Contains(p, "<project_instructions file=\"CLAUDE.md\">\nfrom claude\n") {
		t.Fatalf("CLAUDE.md fallback missing:\n%s", p)
	}

	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("from agents"), 0o644)
	p := a.SystemPrompt(dir, time.Now())
	if !strings.Contains(p, "from agents") || strings.Contains(p, "from claude") {
		t.Fatalf("AGENTS.md must win and CLAUDE.md must be skipped:\n%s", p)
	}
}

// TestLoopRunsToolsAndSendsResults: call 1 asks for read_file, call 2 must carry the
// tool result with the same tool_call_id, and then the model answers.
func TestLoopRunsToolsAndSendsResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	os.WriteFile(path, []byte("hi from file"), 0o644)

	var requests []llm.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		json.NewDecoder(r.Body).Decode(&req)
		requests = append(requests, req)
		if len(requests) == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			argsJSON, _ := json.Marshal(string(args))
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", argsJSON)
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"It says hi.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":4}}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := llm.NewClient("k")
	c.BaseURL = srv.URL
	reg, _ := tools.Default().Subset([]string{"read_file"})
	s := NewSession(&Agent{Name: "general", Model: "m"}, c, reg, "system prompt")
	var events []Event
	s.Emit = func(e Event) { events = append(events, e) }

	if err := s.Send(context.Background(), "read the file"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	second := requests[1].Messages
	// system, user, assistant (tool call), tool (result)
	if len(second) != 4 || second[2].ToolCalls[0].ID != "call_1" || second[3].Role != "tool" || second[3].ToolCallID != "call_1" {
		t.Fatalf("second request messages = %+v", second)
	}
	if want := "     1\thi from file\n"; second[3].Content != want {
		t.Errorf("tool result = %q, want %q", second[3].Content, want)
	}
	if len(requests[1].Tools) != 1 {
		t.Error("tools must be sent on every request")
	}
	if last := s.Messages[len(s.Messages)-1]; last.Content != "It says hi." {
		t.Errorf("last message = %+v", last)
	}
	if _, ok := events[len(events)-1].(TurnDone); !ok {
		t.Errorf("last event = %T, want TurnDone", events[len(events)-1])
	}
}
