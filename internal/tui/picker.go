package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/state"
)

// picker is the full screen /model view: a filter input over a list of models.
type picker struct {
	filter textinput.Model
	items  []llm.ModelInfo // recent models first, then all others, after the filter
	sel    int
	top    int // first visible row
}

func (m *model) fetchModels() tea.Cmd {
	m.modelsLoading = true
	client := m.sess.Client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := client.Models(ctx)
		return modelsMsg{models, err}
	}
}

func (m *model) cmdModel() tea.Cmd {
	if m.busy {
		return m.errorEntry("/model: the agent is working · press esc first")
	}
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "filter: type part of a name, for example sonnet"
	in.CharLimit = 0
	in.Width = max(m.width-6, 10)
	in.Focus()
	m.picker = &picker{filter: in}
	m.picker.refresh(m)
	if m.models == nil && !m.modelsLoading {
		return m.fetchModels() // the first fetch failed: try again
	}
	return nil
}

func (m *model) errorEntry(text string) tea.Cmd {
	m.entries = append(m.entries, entry{kindError, text})
	m.refreshChat()
	return nil
}

// refresh builds the list again after the filter or the model list changed.
func (p *picker) refresh(m *model) {
	byID := map[string]llm.ModelInfo{}
	for _, info := range m.models {
		byID[info.ID] = info
	}
	words := strings.Fields(strings.ToLower(p.filter.Value()))
	seen := map[string]bool{}
	p.items = p.items[:0]
	add := func(info llm.ModelInfo) {
		hay := strings.ToLower(info.ID + " " + info.Name)
		if seen[info.ID] || slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			return
		}
		seen[info.ID] = true
		p.items = append(p.items, info)
	}
	// Recent models show even before the list arrives, so the picker works offline.
	for _, id := range m.opts.State.RecentModels {
		info, ok := byID[id]
		if !ok {
			info = llm.ModelInfo{ID: id}
		}
		add(info)
	}
	for _, info := range m.models {
		add(info)
	}
	p.sel = min(p.sel, max(len(p.items)-1, 0))
	p.keepVisible(m.pickerRows())
}

func (p *picker) move(d, rows int) {
	p.sel = min(max(p.sel+d, 0), max(len(p.items)-1, 0))
	p.keepVisible(rows)
}

func (p *picker) keepVisible(rows int) {
	p.top = min(p.top, p.sel)
	if rows > 0 && p.sel >= p.top+rows {
		p.top = p.sel - rows + 1
	}
}

// pickerRows is the height of the list: the screen minus title, input, column header and footer.
func (m *model) pickerRows() int { return max(m.height-4, 1) }

func (m *model) pickerKey(k tea.KeyMsg) tea.Cmd {
	p, rows := m.picker, m.pickerRows()
	switch k.String() {
	case "esc":
		m.picker = nil
		return nil
	case "up":
		p.move(-1, rows)
	case "down":
		p.move(1, rows)
	case "pgup":
		p.move(-rows, rows)
	case "pgdown":
		p.move(rows, rows)
	case "enter":
		if len(p.items) == 0 {
			return nil
		}
		m.picker = nil
		return m.setModel(p.items[p.sel].ID)
	default:
		before := p.filter.Value()
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(k)
		if p.filter.Value() != before {
			p.sel, p.top = 0, 0
			p.refresh(m)
		}
		return cmd
	}
	return nil
}

// setModel changes the model and saves it as the most recent one.
func (m *model) setModel(id string) tea.Cmd {
	if id == m.modelID {
		return nil
	}
	m.opts.State.AddRecent(id)
	// AddRecent makes a new slice, so the command goroutine can keep this copy while the UI goes on.
	st, path := m.opts.State, m.opts.StatePath
	// Like /clear: busy stays on until the ModelChanged event arrives, so no turn starts in between.
	m.busy = true
	sess := m.sess
	return func() tea.Msg {
		sess.SetModel(id)
		if path == "" {
			return nil
		}
		if err := state.Save(path, st); err != nil {
			return errMsg{fmt.Errorf("save %s: %w", path, err)}
		}
		return nil
	}
}

func (m *model) viewPicker() string {
	p, w := m.picker, m.width
	count := fmt.Sprintf("  %d models · recent first", len(p.items))
	switch {
	case m.modelsLoading:
		count += " · loading the list from OpenRouter…"
	case m.modelsErr != nil:
		count += " · list failed: " + oneLine(m.modelsErr.Error())
	}
	title := ansi.Truncate(titleStyle.Render(" Model")+dimStyle.Render(count), w, "…")

	// Prices and context need room. On a narrow screen the id gets all of it.
	wide := w >= 70
	right := func(ctx, in, out string) string {
		if !wide {
			return ""
		}
		return fmt.Sprintf(" %8s %10s %10s ", ctx, in, out)
	}
	idW := w - 3 - ansi.StringWidth(right("", "", ""))
	header := dimStyle.Render("   " + padRight("model", idW) + right("context", "$/M in", "$/M out"))

	var rows []string
	end := min(p.top+m.pickerRows(), len(p.items))
	for i := p.top; i < end; i++ {
		info := p.items[i]
		mark := "  "
		if info.ID == m.modelID {
			mark = "● "
		}
		ctx, in, out := "", "", ""
		if info.ContextLength > 0 {
			ctx = short(info.ContextLength)
			in, out = price(info.Pricing.Prompt), price(info.Pricing.Completion)
		}
		row := " " + mark + padRight(ansi.Truncate(info.ID, idW, "…"), idW) + right(ctx, in, out)
		if i == p.sel {
			row = selStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if len(p.items) == 0 {
		rows = append(rows, dimStyle.Render("   no model matches"))
	}
	list := lipgloss.NewStyle().Height(m.pickerRows()).Render(strings.Join(rows, "\n"))
	footer := dimStyle.Render(ansi.Truncate(" ↑ ↓ select · enter use · esc cancel · ● current model", w, ""))
	return lipgloss.JoinVertical(lipgloss.Left, title, " "+p.filter.View(), header, list, footer)
}

// short formats a token count: 950, 44k, 200k, 1M, 1.5M.
func short(n int) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

func price(perToken string) string {
	v, ok := llm.PerMillion(perToken)
	switch {
	case !ok:
		return "-"
	case v == 0:
		return "free"
	}
	return fmt.Sprintf("$%.2f", v)
}
