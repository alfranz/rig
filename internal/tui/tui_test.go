package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/alfranz/rig/internal/agent"
	"github.com/alfranz/rig/internal/jsonl"
	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/state"
	"github.com/alfranz/rig/internal/tools"
)

func testModel(t *testing.T, w, h int) *model {
	t.Helper()
	a := &agent.Agent{Name: "general", Model: "test/model", Tools: []string{"read_file"}, Source: "test"}
	sess := agent.NewSession(a, llm.NewClient("k"), tools.Default(), "system")
	m := newModel(sess, Options{LogPath: "/tmp/x.jsonl"})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// checkFits fails if the view is taller or wider than the terminal.
func checkFits(t *testing.T, m *model) string {
	t.Helper()
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) > m.height {
		t.Errorf("view has %d lines, terminal has %d", len(lines), m.height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("line %d is %d wide, terminal is %d: %q", i, w, m.width, ansi.Strip(l))
		}
	}
	return ansi.Strip(v)
}

func feedCall(m *model, id int) {
	m.Update(agent.CallStarted{CallID: id, Request: []byte(`{"model":"test/model","messages":[{"role":"user","content":"hi"}],"tools":[]}`), At: time.Now()})
	m.Update(agent.StreamLine{CallID: id, Line: ": OPENROUTER PROCESSING", Offset: 100 * time.Millisecond})
	m.Update(agent.StreamLine{CallID: id, Line: `data: {"choices":[{"delta":{"content":"Hello"}}]}`, Offset: 300 * time.Millisecond})
	m.Update(agent.TextDelta{CallID: id, Text: "Hello there"})
	m.Update(agent.CallFinished{CallID: id, Result: &llm.Result{
		Status: 200, FinishReason: "tool_calls", Duration: 1500 * time.Millisecond,
		Message: llm.Message{Role: "assistant", Content: "Hello there"},
		Usage:   &llm.Usage{PromptTokens: 1423, CompletionTokens: 162, TotalTokens: 1585, Cost: 0.0045},
	}})
}

func TestLayoutAndEvents(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 15}} {
		m := testModel(t, size[0], size[1])
		if v := checkFits(t, m); !strings.Contains(v, "No calls yet") {
			t.Errorf("%v: empty view missing placeholder:\n%s", size, v)
		}
		feedCall(m, 1)
		m.Update(agent.ToolStarted{CallID: 1, ToolCallID: "c1", Name: "read_file", Args: `{"path":"go.mod"}`})
		m.Update(agent.ToolFinished{ToolCallID: "c1", Name: "read_file", Output: "     1\tmodule rig\n", Duration: 2 * time.Millisecond})
		m.Update(agent.TurnDone{})

		v := checkFits(t, m)
		if size[0] < 80 {
			continue // too narrow to show every column
		}
		for _, want := range []string{"Hello there", "⚙ read_file", "#1", "200", "in  1,423", "$0.0045"} {
			if !strings.Contains(v, want) {
				t.Errorf("%v: view missing %q:\n%s", size, want, v)
			}
		}
	}
}

func TestClickOpensDetailTabs(t *testing.T) {
	m := testModel(t, 120, 40)
	feedCall(m, 1)
	feedCall(m, 2)

	// Click the first row of the calls pane: border row 0, title row 1, first call row 2.
	m.Update(tea.MouseMsg{X: m.chatWidth() + 3, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.detail || m.sel != 0 {
		t.Fatalf("detail=%v sel=%d, want detail open on call 0", m.detail, m.sel)
	}
	v := checkFits(t, m)
	if !strings.Contains(v, "Call #1") || !strings.Contains(v, "POST https://openrouter.ai/api/v1/chat/completions") || !strings.Contains(v, "1 messages") {
		t.Errorf("request tab:\n%s", v)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if v := checkFits(t, m); !strings.Contains(v, "+ 0.100s  : OPENROUTER PROCESSING") {
		t.Errorf("stream tab:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if v := checkFits(t, m); !strings.Contains(v, "prompt_tokens (in):") || !strings.Contains(v, "1,423") {
		t.Errorf("result tab:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.detail {
		t.Fatal("esc should close the detail view")
	}
}

func TestApprovalPrompt(t *testing.T) {
	m := testModel(t, 100, 30)
	reply := make(chan bool, 1)
	m.Update(approvalMsg{name: "bash", args: `{"command":"go test ./..."}`, reply: reply})
	if v := checkFits(t, m); !strings.Contains(v, `Allow bash {"command":"go test ./..."}`) {
		t.Errorf("approval prompt missing:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if ok := <-reply; !ok || m.approval != nil {
		t.Fatalf("reply=%v approval=%v", ok, m.approval)
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := num(n); got != want {
			t.Errorf("num(%d) = %q, want %q", n, got, want)
		}
	}
}

func typeText(m *model, s string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func TestSlashCommandMenu(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 15}} {
		m := testModel(t, size[0], size[1])
		typeText(m, "/")
		v := checkFits(t, m)
		for _, want := range []string{"/clear", "/exit", "(also /quit)"} {
			if size[0] >= 80 && !strings.Contains(v, want) {
				t.Errorf("%v: menu missing %q:\n%s", size, want, v)
			}
		}
		typeText(m, "q") // matches the alias /quit
		if got := m.menuMatches(); len(got) != 1 || got[0].name != "exit" {
			t.Errorf("%v: /q matches %v, want only exit", size, got)
		}
		checkFits(t, m)
	}
}

func TestClearCommand(t *testing.T) {
	m := testModel(t, 100, 30)
	var events []agent.Event
	m.sess.Emit = func(e agent.Event) { events = append(events, e) }
	m.sess.Messages = append(m.sess.Messages, llm.Message{Role: "user", Content: "hi"})
	feedCall(m, 1)

	typeText(m, "/cl")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy || m.menuRows != 0 {
		t.Fatalf("enter on /cl: cmd=%v busy=%v menuRows=%d", cmd != nil, m.busy, m.menuRows)
	}
	cmd()
	if len(m.sess.Messages) != 1 || len(events) != 1 {
		t.Fatalf("after reset: %d messages, events %v", len(m.sess.Messages), events)
	}
	m.Update(events[0])
	v := checkFits(t, m)
	if m.busy || !strings.Contains(v, "context cleared") || strings.Contains(v, "Hello there") {
		t.Errorf("busy=%v view:\n%s", m.busy, v)
	}
	if !strings.Contains(v, "#1") || !strings.Contains(v, "$0.0045") {
		t.Errorf("calls pane and cost should stay:\n%s", v)
	}
}

func TestUnknownAndExitCommands(t *testing.T) {
	m := testModel(t, 100, 30)
	typeText(m, "/nope now")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := checkFits(t, m); m.busy || !strings.Contains(v, "unknown command /nope") {
		t.Errorf("busy=%v view:\n%s", m.busy, v)
	}
	typeText(m, "/quit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/quit returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("/quit should quit")
	}
}

var testModels = []llm.ModelInfo{
	{ID: "anthropic/claude-sonnet-5.5", Name: "Anthropic: Claude Sonnet 5.5", ContextLength: 1_000_000,
		Pricing: llm.Pricing{Prompt: "0.000003", Completion: "0.000015"}},
	{ID: "openai/gpt-6", Name: "OpenAI: GPT-6", ContextLength: 400_000,
		Pricing: llm.Pricing{Prompt: "0.00000125", Completion: "0.00001"}},
	{ID: "test/model", Name: "Test", ContextLength: 128_000, Pricing: llm.Pricing{Prompt: "0", Completion: "0"}},
}

func TestModelPicker(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 15}} {
		m := testModel(t, size[0], size[1])
		m.opts.StatePath = filepath.Join(t.TempDir(), "state.json")
		m.opts.State.RecentModels = []string{"openai/gpt-6", "gone/model"}
		var events []agent.Event
		m.sess.Emit = func(e agent.Event) { events = append(events, e) }

		typeText(m, "/model")
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.picker == nil {
			t.Fatalf("%v: picker did not open", size)
		}
		m.Update(modelsMsg{models: testModels})
		v := checkFits(t, m)
		// Recent first (also one that OpenRouter no longer lists), then the rest.
		if ids := pickerIDs(m); strings.Join(ids, " ") != "openai/gpt-6 gone/model anthropic/claude-sonnet-5.5 test/model" {
			t.Errorf("%v: order %v", size, ids)
		}
		if size[0] >= 80 && (!strings.Contains(v, "$3.00") || !strings.Contains(v, "1M") || !strings.Contains(v, "free")) {
			t.Errorf("%v: picker missing price or context:\n%s", size, v)
		}

		typeText(m, "son 5.5") // every word must match
		if ids := pickerIDs(m); len(ids) != 1 || ids[0] != "anthropic/claude-sonnet-5.5" {
			t.Fatalf("%v: filter gave %v", size, ids)
		}
		checkFits(t, m)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.picker != nil || cmd == nil || !m.busy {
			t.Fatalf("%v: enter: picker=%v cmd=%v busy=%v", size, m.picker != nil, cmd != nil, m.busy)
		}
		if msg := cmd(); msg != nil {
			t.Fatalf("%v: set model: %v", size, msg)
		}
		if len(events) != 1 {
			t.Fatalf("%v: events %v", size, events)
		}
		m.Update(events[0])
		v = checkFits(t, m)
		if m.busy || m.modelID != "anthropic/claude-sonnet-5.5" || m.sess.Agent.Model != m.modelID {
			t.Errorf("%v: busy=%v modelID=%q", size, m.busy, m.modelID)
		}
		if size[0] >= 120 && !strings.Contains(v, "model: test/model → anthropic/claude-sonnet-5.5") {
			t.Errorf("%v: chat missing model change:\n%s", size, v)
		}
		saved, err := state.Load(m.opts.StatePath)
		if err != nil || strings.Join(saved.RecentModels, " ") != "anthropic/claude-sonnet-5.5 openai/gpt-6 gone/model" {
			t.Errorf("%v: saved %v, %v", size, saved.RecentModels, err)
		}
	}
}

func pickerIDs(m *model) []string {
	var ids []string
	for _, info := range m.picker.items {
		ids = append(ids, info.ID)
	}
	return ids
}

func TestModelPickerEscAndBusy(t *testing.T) {
	m := testModel(t, 100, 30)
	typeText(m, "/models")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := checkFits(t, m); m.picker == nil || !strings.Contains(v, "loading") {
		t.Fatalf("picker before the list arrives:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.picker != nil {
		t.Fatal("esc should close the picker")
	}
	m.busy = true
	typeText(m, "/model")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := checkFits(t, m); m.picker != nil || !strings.Contains(v, "/model: the agent is working") {
		t.Errorf("picker opened while busy:\n%s", v)
	}
}

func TestContextView(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 15}} {
		m := testModel(t, size[0], size[1])
		m.Update(modelsMsg{models: testModels}) // test/model has 128k context: smaller than the 200k default
		m.sess.Messages[0].Content = "You are an agent.\n\n<env>\nos: x\n</env>\n\n<project_instructions file=\"AGENTS.md\">\nrules\n</project_instructions>"
		m.sess.Messages = append(m.sess.Messages,
			llm.Message{Role: "user", Content: "hi"},
			llm.Message{Role: "assistant", Content: "Hello there"})
		feedCall(m, 1) // usage: 1,423 in + 162 out

		v := checkFits(t, m)
		if size[0] >= 120 && !strings.Contains(v, "ctx 2k/128k") {
			t.Errorf("%v: status bar missing ctx:\n%s", size, v)
		}
		typeText(m, "/context")
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = checkFits(t, m)
		for _, want := range []string{"1,585 / 128,000 tokens", "■", "agent prompt", "tool definitions"} {
			if size[0] >= 80 && !strings.Contains(v, want) {
				t.Errorf("%v: context view missing %q:\n%s", size, want, v)
			}
		}
		m.pageVP.GotoBottom()
		if v := checkFits(t, m); !strings.Contains(oneLine(v), "model context 128,000") {
			t.Errorf("%v: context view bottom:\n%s", size, v)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.page != "" {
			t.Errorf("%v: esc should close the page", size)
		}
	}
}

func TestContextParts(t *testing.T) {
	m := testModel(t, 100, 30)
	m.sess.Messages[0].Content = "prompt\n\n<env>\nx\n</env>"
	m.sess.Messages = append(m.sess.Messages,
		llm.Message{Role: "user", Content: "12345"},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function"}}},
		llm.Message{Role: "tool", ToolCallID: "c1", Content: "abc"})
	got := map[string]int{}
	for _, p := range m.contextParts() {
		got[p.name] = p.bytes
	}
	if got["agent prompt"] != 6 || got["environment"] != 16 || got["project instructions"] != 0 ||
		got["user messages"] != 5 || got["assistant messages"] == 0 || got["tool results"] != 3 || got["tool definitions"] == 0 {
		t.Errorf("parts = %v", got)
	}
}

func TestUsageView(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 15}} {
		m := testModel(t, size[0], size[1])
		u := jsonl.Usage{
			All:    jsonl.Total{Calls: 3, In: 3000, Out: 300, Cost: 0.76},
			Days:   []jsonl.Total{{Key: "2026-10-18", Calls: 2, Cost: 0.75}, {Key: "2026-10-17", Calls: 1, Cost: 0.01}},
			Models: []jsonl.Total{{Key: "anthropic/claude-sonnet-5.5-with-a-very-long-name", Calls: 2, Cost: 0.75}},
			Files:  2,
		}
		m.Update(usageMsg{usage: u, dir: "/tmp/logs"})
		v := checkFits(t, m)
		for _, want := range []string{"Usage", "$0.7600", "Per day", "2026-10-18"} {
			if !strings.Contains(v, want) {
				t.Errorf("%v: usage view missing %q:\n%s", size, want, v)
			}
		}
	}
}
