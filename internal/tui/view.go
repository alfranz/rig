package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colorAccent = lipgloss.Color("12")
	colorDim    = lipgloss.Color("8")

	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	agentStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dimStyle    = lipgloss.NewStyle().Foreground(colorDim)
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	titleStyle  = lipgloss.NewStyle().Bold(true)
	selStyle    = lipgloss.NewStyle().Reverse(true)
	selDimStyle = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	tabOn       = lipgloss.NewStyle().Reverse(true).Bold(true).Padding(0, 1)
	tabOff      = lipgloss.NewStyle().Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	warnStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	cmdStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
)

func pane(focused bool) lipgloss.Style {
	c := colorDim
	if focused {
		c = colorAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).Padding(0, 1)
}

func (m *model) View() string {
	if !m.ready {
		return "starting…"
	}
	switch {
	case m.picker != nil:
		return m.viewPicker()
	case m.page != "":
		return m.viewPage()
	case m.detail:
		return m.viewDetail()
	}
	chatW, topH := m.chatWidth(), m.topHeight()
	chat := pane(!m.focusCalls).Width(chatW - 2).Height(topH - 2).
		Render(titleStyle.Render("Chat") + "\n" + m.chat.View())
	calls := pane(m.focusCalls).Width(m.width - chatW - 2).Height(topH - 2).
		Render(m.viewCalls(m.width - chatW - 4))
	top := lipgloss.JoinHorizontal(lipgloss.Top, chat, calls)
	parts := []string{top}
	if m.menuRows > 0 {
		parts = append(parts, m.viewMenu())
	}
	return lipgloss.JoinVertical(lipgloss.Left, append(parts, m.viewInput(), m.viewStatus())...)
}

func (m *model) renderEntries(w int) string {
	name := m.sess.Agent.Name
	var parts []string
	for _, e := range m.entries {
		var s string
		switch e.kind {
		case kindUser:
			s = "\n" + userStyle.Render("you") + " " + e.text
		case kindAssistant:
			s = "\n" + agentStyle.Render(name) + " " + e.text
		case kindReasoning:
			s = dimStyle.Italic(true).Render("thinking: " + e.text)
		case kindTool:
			s = toolStyle.Render(ansi.Truncate("⚙ "+oneLine(e.text), w, "…"))
		case kindToolResult:
			s = dimStyle.Render(ansi.Truncate("  ↳ "+e.text, w, "…"))
		case kindToolError:
			s = errorStyle.Render(ansi.Truncate("  ↳ "+e.text, w, "…"))
		case kindError:
			s = "\n" + errorStyle.Bold(true).Render("error: "+e.text)
		default:
			s = dimStyle.Render(e.text)
		}
		parts = append(parts, wrap(s, w))
	}
	return strings.Join(parts, "\n")
}

func (m *model) viewCalls(w int) string {
	var b strings.Builder
	// The title must stay on one line: mouse clicks map screen rows to calls.
	hint := ansi.Truncate("  click or enter to inspect", max(w-9, 0), "")
	b.WriteString(titleStyle.Render("API calls") + dimStyle.Render(hint))
	if len(m.calls) == 0 {
		b.WriteString("\n" + dimStyle.Render("No calls yet."))
		return b.String()
	}
	end := min(m.callsTop+m.callRows(), len(m.calls))
	for i := m.callsTop; i < end; i++ {
		row := ansi.Truncate(callRow(m.calls[i]), w, "…")
		row += strings.Repeat(" ", max(w-ansi.StringWidth(row), 0))
		switch {
		case i == m.sel && m.focusCalls:
			row = selStyle.Render(row)
		case i == m.sel:
			row = selDimStyle.Render(row)
		case m.calls[i].err != nil:
			row = errorStyle.Render(row)
		}
		b.WriteString("\n" + row)
	}
	return b.String()
}

// callRow is one line in the calls pane: id, HTTP status, time, tokens in/out, finish reason.
func callRow(c *callView) string {
	if !c.done {
		return fmt.Sprintf("#%-3d  …  streaming  %d lines", c.id, len(c.lines))
	}
	status, finish := "ERR", "error"
	in, out, dur := "-", "-", "-"
	if r := c.res; r != nil {
		if r.Status != 0 {
			status = fmt.Sprint(r.Status)
		}
		if r.FinishReason != "" && c.err == nil {
			finish = r.FinishReason
		}
		dur = fmt.Sprintf("%.1fs", r.Duration.Seconds())
		if u := r.Usage; u != nil {
			in, out = num(u.PromptTokens), num(u.CompletionTokens)
		}
	}
	return fmt.Sprintf("#%-2d %3s %5s in %6s out %5s %s", c.id, status, dur, in, out, finish)
}

func (m *model) viewInput() string {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorDim).
		Padding(0, 1).Width(m.width - 2)
	if a := m.approval; a != nil {
		q := fmt.Sprintf("Allow %s %s ?  y = yes, n = no", a.name, oneLine(a.args))
		return box.BorderForeground(lipgloss.Color("3")).Render(warnStyle.Render(ansi.Truncate(q, m.width-6, "…")))
	}
	return box.Render(m.input.View())
}

func (m *model) viewStatus() string {
	state := "ready"
	switch {
	case m.approval != nil:
		state = "waiting for your y/n"
	case m.busy:
		state = "working… esc stops"
	}
	zone, _ := m.smartZone()
	left := fmt.Sprintf(" %s · %s · ctx %s/%s · %d calls · in %s  out %s · $%.4f",
		m.sess.Agent.Name, m.modelID, short(m.ctxTokens), short(zone), len(m.calls), num(m.totalIn), num(m.totalOut), m.totalCost)
	right := state + " "
	gap := max(m.width-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return statusStyle.Render(ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, ""))
}

func (m *model) viewDetail() string {
	c := m.calls[m.sel]
	head := fmt.Sprintf(" Call #%d ", c.id)
	if c.res != nil {
		head += fmt.Sprintf("· HTTP %d · %.2fs ", c.res.Status, c.res.Duration.Seconds())
	}
	var tabs []string
	for i, n := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if tab(i) == m.detailTab {
			tabs = append(tabs, tabOn.Render(label))
		} else {
			tabs = append(tabs, tabOff.Render(label))
		}
	}
	header := titleStyle.Render(head) + " " + strings.Join(tabs, " ")
	rule := dimStyle.Render(strings.Repeat("─", m.width))
	footer := dimStyle.Render(" tab / ← → switch tabs · ↑ ↓ pgup pgdn scroll · esc back")
	return lipgloss.JoinVertical(lipgloss.Left, header, rule, m.detailVP.View(), footer)
}

func (m *model) detailContent(c *callView) string {
	switch m.detailTab {
	case tabRequest:
		return requestContent(m.sess.Client.BaseURL, c.request)
	case tabStream:
		return streamContent(c)
	default:
		return resultContent(c)
	}
}

func requestContent(baseURL string, body []byte) string {
	var shape struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	json.Unmarshal(body, &shape)
	var b strings.Builder
	fmt.Fprintf(&b, "POST %s/chat/completions\n", baseURL)
	b.WriteString("Authorization: Bearer sk-or-… (hidden)\nContent-Type: application/json\nAccept: text/event-stream\n\n")
	fmt.Fprintf(&b, "%s bytes · %d messages · %d tools. The whole conversation is sent again on every call.\n\n",
		num(len(body)), len(shape.Messages), len(shape.Tools))
	b.WriteString(prettyJSON(body))
	return b.String()
}

func streamContent(c *callView) string {
	if len(c.lines) == 0 {
		return dimStyle.Render("Waiting for the first byte…")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d lines. Time is measured from when the request was sent.\n", len(c.lines))
	b.WriteString(dimStyle.Render("Lines that start with ':' are keep-alive comments. 'data: [DONE]' ends the stream.") + "\n\n")
	for _, l := range c.lines {
		fmt.Fprintf(&b, "%s  %s\n", dimStyle.Render(fmt.Sprintf("+%6.3fs", l.Offset.Seconds())), l.Line)
	}
	return b.String()
}

func resultContent(c *callView) string {
	if !c.done {
		return dimStyle.Render(fmt.Sprintf("Streaming… %d lines so far. See the Stream tab.", len(c.lines)))
	}
	var b strings.Builder
	if c.err != nil {
		b.WriteString(errorStyle.Render("error: "+c.err.Error()) + "\n\n")
	}
	r := c.res
	if r == nil {
		return b.String()
	}
	kv := func(k, v string) { fmt.Fprintf(&b, "%-26s %s\n", k+":", v) }
	kv("http status", fmt.Sprint(r.Status))
	kv("finish_reason", r.FinishReason)
	kv("model", r.Model)
	kv("provider", r.Provider)
	kv("generation id", r.ID)
	kv("time to first token", secs(r.TTFT))
	kv("total time", secs(r.Duration))

	b.WriteString("\n" + titleStyle.Render("usage") + "\n")
	if u := r.Usage; u != nil {
		kv("prompt_tokens (in)", num(u.PromptTokens))
		kv("completion_tokens (out)", num(u.CompletionTokens))
		kv("total_tokens", num(u.TotalTokens))
		if d := u.PromptTokensDetails; d != nil {
			kv("cached tokens", num(d.CachedTokens))
			kv("cache write tokens", num(d.CacheWriteTokens))
		}
		if d := u.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
			kv("reasoning tokens", num(d.ReasoningTokens))
		}
		kv("cost", fmt.Sprintf("$%.6f", u.Cost))
		b.WriteString(dimStyle.Render("'in' counts everything sent: system prompt, tool definitions and all earlier messages.") + "\n")
	} else {
		b.WriteString(dimStyle.Render("no usage block in the stream") + "\n")
	}

	if r.Reasoning != "" {
		b.WriteString("\n" + titleStyle.Render("reasoning") + "\n" + r.Reasoning + "\n")
	}
	b.WriteString("\n" + titleStyle.Render("assembled assistant message") +
		dimStyle.Render("  (added to the conversation and sent back on the next call)") + "\n")
	msg, _ := json.MarshalIndent(r.Message, "", "  ")
	b.WriteString(string(msg))
	return b.String()
}

// summarize turns a tool result into one short line for the chat pane.
func summarize(out string, d time.Duration) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	first = oneLine(first) // tabs are wider on screen than ansi.StringWidth counts
	lines := strings.Count(out, "\n") + 1
	return fmt.Sprintf("%s  (%d lines, %s bytes, %s)", first, lines, num(len(out)), d.Round(time.Millisecond))
}

// prettyJSON indents JSON and keeps the key order, so it matches what went over the wire.
func prettyJSON(b []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return string(b)
	}
	return out.String()
}

func wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func secs(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }

// num formats 12345 as "12,345".
func num(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + num(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
