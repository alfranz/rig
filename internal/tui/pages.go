package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/alfranz/rig/internal/jsonl"
)

// openPage shows a full screen text view. render runs again when the terminal size changes.
func (m *model) openPage(title string, render func(w int) string) {
	m.page, m.pageRender = title, render
	m.pageVP.SetContent(render(m.width))
	m.pageVP.GotoTop()
}

func (m *model) viewPage() string {
	header := titleStyle.Render(" " + m.page + " ")
	rule := dimStyle.Render(strings.Repeat("─", m.width))
	footer := dimStyle.Render(ansi.Truncate(" ↑ ↓ pgup pgdn scroll · esc back", m.width, ""))
	return lipgloss.JoinVertical(lipgloss.Left, header, rule, m.pageVP.View(), footer)
}

// fitLines cuts every line to w, so each line is one screen row.
func fitLines(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(strings.ReplaceAll(l, "\t", "  "), w, "…")
	}
	return strings.Join(lines, "\n")
}

// ── /context ──

// ctxPart is one part of what we send on every request.
type ctxPart struct {
	name  string
	bytes int
	color lipgloss.Color
}

const gridCols, gridRows = 10, 10

func (m *model) cmdContext() tea.Cmd {
	if m.busy {
		// The loop appends to Messages while it runs, so we may not read them now.
		return m.errorEntry("/context: the agent is working · press esc first")
	}
	parts := m.contextParts()
	total, measured := m.ctxTokens, true
	if total == 0 {
		var bytes int
		for _, p := range parts {
			bytes += p.bytes
		}
		total, measured = bytes/4, false
	}
	zone, zoneNote := m.smartZone()
	m.openPage("Context", func(w int) string { return renderContext(parts, total, measured, zone, zoneNote, w) })
	return nil
}

// contextParts splits the conversation into parts and measures each one in bytes.
func (m *model) contextParts() []ctxPart {
	sys := m.sess.Messages[0].Content
	// SystemPrompt builds the system message as: agent prompt, <env>, <project_instructions>.
	env := strings.Index(sys, "\n\n<env>")
	instr := strings.Index(sys, "\n\n<project_instructions")
	if instr < 0 {
		instr = len(sys)
	}
	if env < 0 {
		env = instr
	}
	tools, _ := json.Marshal(m.sess.Tools.Definitions())
	parts := []ctxPart{
		{"agent prompt", env, "5"},
		{"environment", instr - env, "4"},
		{"project instructions", len(sys) - instr, "6"},
		{"tool definitions", len(tools), "3"},
		{"user messages", 0, "2"},
		{"assistant messages", 0, "13"},
		{"tool results", 0, "9"},
	}
	for _, msg := range m.sess.Messages[1:] {
		n := len(msg.Content)
		switch msg.Role {
		case "user":
			parts[4].bytes += n
		case "assistant":
			calls, _ := json.Marshal(msg.ToolCalls)
			parts[5].bytes += n + len(calls)
		case "tool":
			parts[6].bytes += n
		}
	}
	return parts
}

// smartZone is min(smart_zone_tokens, the model's context length).
func (m *model) smartZone() (int, string) {
	zone := m.opts.State.SmartZone()
	note := fmt.Sprintf("smart_zone_tokens %s", num(zone))
	for _, info := range m.models {
		if info.ID == m.modelID && info.ContextLength > 0 {
			note += fmt.Sprintf(", model context %s", num(info.ContextLength))
			zone = min(zone, info.ContextLength)
		}
	}
	return zone, note
}

func renderContext(parts []ctxPart, total int, measured bool, zone int, zoneNote string, w int) string {
	var bytes int
	for _, p := range parts {
		bytes += p.bytes
	}
	// Each part gets its share of the bytes, scaled to the token total.
	tokens := make([]int, len(parts))
	for i, p := range parts {
		if bytes > 0 {
			tokens[i] = total * p.bytes / bytes
		}
	}

	// The grid is the smart zone. Each cell is zone/100 tokens. Cells fill in part order.
	cells := gridCols * gridRows
	end := make([]int, len(parts)) // part i fills the cells before end[i]
	cum := 0
	for i, t := range tokens {
		cum += t
		end[i] = min(cells, cum*cells/max(zone, 1))
	}
	var grid []string
	var row strings.Builder
	for c := range cells {
		cell := dimStyle.Render("·")
		for i, e := range end {
			if c < e {
				cell = lipgloss.NewStyle().Foreground(parts[i].color).Render("■")
				break
			}
		}
		row.WriteString(cell + " ")
		if (c+1)%gridCols == 0 {
			grid = append(grid, row.String())
			row.Reset()
		}
	}

	pct := func(n int) string { return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(max(zone, 1))) }
	legend := []string{
		titleStyle.Render(fmt.Sprintf("%s / %s tokens (%s)", num(total), num(zone), pct(total))),
		"",
	}
	for i, p := range parts {
		dot := lipgloss.NewStyle().Foreground(p.color).Render("■")
		legend = append(legend, fmt.Sprintf("%s %-21s %8s  %6s", dot, p.name, num(tokens[i]), pct(tokens[i])))
	}
	free := max(zone-total, 0)
	legend = append(legend, fmt.Sprintf("%s %-21s %8s  %6s", dimStyle.Render("·"), "free", num(free), pct(free)))

	var b strings.Builder
	gridW := gridCols * 2
	if w >= gridW+3+50 {
		// Legend next to the grid.
		for i := range max(len(grid), len(legend)) {
			g := strings.Repeat(" ", gridW)
			if i < len(grid) {
				g = grid[i]
			}
			l := ""
			if i < len(legend) {
				l = legend[i]
			}
			b.WriteString(" " + g + "  " + l + "\n")
		}
	} else {
		for _, g := range grid {
			b.WriteString(" " + g + "\n")
		}
		b.WriteString("\n" + strings.Join(legend, "\n") + "\n")
	}

	// Grid and legend rows are cut to the width. The notes below are prose, so they wrap.
	var notes []string
	if total > zone {
		notes = append(notes, warnStyle.Render("The context is larger than the smart zone. Answers may get worse. /clear starts fresh."))
	}
	if measured {
		notes = append(notes, "The total comes from the last call's usage: prompt + completion tokens, without reasoning.")
	} else {
		notes = append(notes, "No calls yet. The total is an estimate: 4 bytes per token.")
	}
	notes = append(notes,
		"The split is an estimate: each part's share of the bytes, scaled to the total.",
		"Smart zone = min("+zoneNote+"). Set smart_zone_tokens in ~/.rig/state.json.")
	return fitLines(b.String(), w) + "\n" + wrap(dimStyle.Render(strings.Join(notes, "\n")), w)
}

// ── /usage ──

type usageMsg struct {
	usage jsonl.Usage
	dir   string
	err   error
}

func (m *model) cmdUsage() tea.Cmd {
	dir := filepath.Dir(m.opts.LogPath)
	// Reading all logs can take a moment, so it runs outside Update.
	return func() tea.Msg {
		u, err := jsonl.Summarize(dir)
		return usageMsg{u, dir, err}
	}
}

func (m *model) openUsage(msg usageMsg) {
	if msg.err != nil {
		m.errorEntry("/usage: " + msg.err.Error())
		return
	}
	m.openPage("Usage", func(w int) string { return renderUsage(msg.usage, msg.dir, w) })
}

func renderUsage(u jsonl.Usage, dir string, w int) string {
	var b strings.Builder
	a := u.All
	fmt.Fprintf(&b, "%s  %s calls · in %s · out %s\n",
		titleStyle.Render(fmt.Sprintf("$%.4f", a.Cost)), num(a.Calls), num(a.In), num(a.Out))
	note := wrap(dimStyle.Render(fmt.Sprintf("From %d log files in %s. Cost is what OpenRouter reported in each call's usage block.", u.Files, dir)), w)

	table := func(title string, rows []jsonl.Total, keyW int) {
		maxCost := 0.0
		for _, r := range rows {
			maxCost = max(maxCost, r.Cost)
		}
		fmt.Fprintf(&b, "\n%s\n", titleStyle.Render(title))
		head := fmt.Sprintf("%s %6s %11s %9s %10s", padRight("", keyW), "calls", "in", "out", "cost")
		b.WriteString(dimStyle.Render(head) + "\n")
		barW := max(w-ansi.StringWidth(head)-2, 0)
		for _, r := range rows {
			bar := ""
			if maxCost > 0 {
				bar = strings.Repeat("█", int(float64(barW)*r.Cost/maxCost))
			}
			key := padRight(ansi.Truncate(r.Key, keyW, "…"), keyW)
			fmt.Fprintf(&b, "%s %6s %11s %9s %10s  %s\n", key, num(r.Calls), num(r.In), num(r.Out),
				fmt.Sprintf("$%.4f", r.Cost), cmdStyle.Render(bar))
		}
	}
	if a.Calls == 0 {
		b.WriteString("\n" + dimStyle.Render("No API calls logged yet.") + "\n")
	} else {
		table("Per day", u.Days, 10)
		table("Per model", u.Models, 32)
	}
	return fitLines(b.String(), w) + "\n" + note
}
