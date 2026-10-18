package tui

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/alfranz/rig/internal/jsonl"
	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/state"
)

// picker is the full screen /model view: a filter input, a list of models, and a card for the selected one.
type picker struct {
	filter textinput.Model
	sort   sortMode
	items  []llm.ModelInfo // after filter and sort
	sel    int
	top    int // first visible row
}

type sortMode int

const (
	sortRecent sortMode = iota // your recent models, then OpenRouter's order (newest first)
	sortUsed                   // your calls per model, from the logs
	sortIntelligence
	sortPrice
)

var sortNames = []string{"recent", "most used", "intelligence", "price"}

// usedMsg has the number of calls per model in all logs.
type usedMsg struct{ counts map[string]int }

// openURL opens a link in the browser. Tests replace it.
var openURL = func(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	}
	return exec.Command("xdg-open", url).Run()
}

func modelURL(id string) string { return "https://openrouter.ai/" + id }

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

// loadUsed counts calls per model once at startup. After that, CallFinished adds to the counts.
func (m *model) loadUsed() tea.Cmd {
	dir := filepath.Dir(m.opts.LogPath)
	return func() tea.Msg {
		u, _ := jsonl.Summarize(dir) // no counts is fine: the column shows "-"
		counts := map[string]int{}
		for _, t := range u.Models {
			counts[t.Key] = t.Calls
		}
		return usedMsg{counts}
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

// refresh builds the list again after the filter, the sort or the model list changed.
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
		if ok && !info.SupportsTools() {
			continue
		}
		if !ok {
			info = llm.ModelInfo{ID: id}
		}
		add(info)
	}
	for _, info := range m.models {
		if info.SupportsTools() {
			add(info)
		}
	}

	// SortStableFunc keeps the recent order for equal values. Models without a value go last.
	switch p.sort {
	case sortUsed:
		slices.SortStableFunc(p.items, func(a, b llm.ModelInfo) int { return cmp.Compare(m.used[b.ID], m.used[a.ID]) })
	case sortIntelligence:
		slices.SortStableFunc(p.items, func(a, b llm.ModelInfo) int {
			return cmp.Compare(deref(b.Intelligence(), -1), deref(a.Intelligence(), -1))
		})
	case sortPrice:
		slices.SortStableFunc(p.items, func(a, b llm.ModelInfo) int {
			pa, oka := llm.PerMillion(a.Pricing.Prompt)
			pb, okb := llm.PerMillion(b.Pricing.Prompt)
			return cmp.Or(compareBool(okb, oka), cmp.Compare(pa, pb))
		})
	}
	p.sel = min(p.sel, max(len(p.items)-1, 0))
	p.keepVisible(m.pickerRows())
}

func deref(f *float64, missing float64) float64 {
	if f == nil {
		return missing
	}
	return *f
}

// compareBool sorts false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
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

// cardRows is the height of the card below the list. A short terminal drops the card and keeps the list.
func (m *model) cardRows() int {
	if m.height < 16 {
		return 0
	}
	return 6
}

// pickerRows is the height of the list: the screen minus title, input, column header, card and footer.
func (m *model) pickerRows() int { return max(m.height-4-m.cardRows(), 1) }

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
	case "tab":
		p.sort = (p.sort + 1) % sortMode(len(sortNames))
		p.sel, p.top = 0, 0
		p.refresh(m)
	case "ctrl+o":
		if len(p.items) == 0 {
			return nil
		}
		url := modelURL(p.items[p.sel].ID)
		return func() tea.Msg {
			if err := openURL(url); err != nil {
				return errMsg{fmt.Errorf("open %s: %w", url, err)}
			}
			return nil
		}
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
	info := fmt.Sprintf("  %d with tools · sort: %s (tab)", len(p.items), sortNames[p.sort])
	switch {
	case m.modelsLoading:
		info += " · loading the list from OpenRouter…"
	case m.modelsErr != nil:
		info += " · list failed: " + oneLine(m.modelsErr.Error())
	}
	title := ansi.Truncate(titleStyle.Render(" Model")+dimStyle.Render(info), w, "…")

	// The numbers need room. On a narrow screen the id gets all of it.
	wide := w >= 76
	right := func(ctx, iq, in, out, used string) string {
		if !wide {
			return ""
		}
		return fmt.Sprintf(" %5s %5s %8s %8s %5s ", ctx, iq, in, out, used)
	}
	idW := w - 3 - ansi.StringWidth(right("", "", "", "", ""))
	header := dimStyle.Render("   " + padRight("model", idW) + right("ctx", "iq", "$/M in", "$/M out", "used"))

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
		used := "-"
		if n := m.used[info.ID]; n > 0 {
			used = num(n)
		}
		row := " " + mark + padRight(ansi.Truncate(info.ID, idW, "…"), idW) + right(ctx, score(info.Intelligence()), in, out, used)
		if i == p.sel {
			row = selStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if len(p.items) == 0 {
		rows = append(rows, dimStyle.Render("   no model matches"))
	}
	list := lipgloss.NewStyle().Height(m.pickerRows()).Render(strings.Join(rows, "\n"))
	footer := dimStyle.Render(ansi.Truncate(" ↑ ↓ select · enter use · tab sort · ctrl+o open page · esc cancel · ● current", w, ""))
	parts := []string{title, " " + p.filter.View(), header, list}
	if m.cardRows() > 0 {
		parts = append(parts, m.viewCard())
	}
	return lipgloss.JoinVertical(lipgloss.Left, append(parts, footer)...)
}

// viewCard shows the selected model: name, benchmarks, limits, and the start of the description.
func (m *model) viewCard() string {
	w := m.width
	lines := []string{dimStyle.Render(strings.Repeat("─", w))}
	p := m.picker
	if len(p.items) == 0 {
		return lipgloss.NewStyle().Height(m.cardRows()).Render(lines[0])
	}
	info := p.items[p.sel]
	lines = append(lines, " "+titleStyle.Render(info.ID)+"  "+dimStyle.Render(info.Name))
	if info.ContextLength == 0 {
		lines = append(lines, dimStyle.Render(" Not in the OpenRouter list. It may be gone, or the list has not loaded yet."))
		return lipgloss.NewStyle().Height(m.cardRows()).Render(fitLines(strings.Join(lines, "\n"), w))
	}

	if aa := info.Benchmarks.ArtificialAnalysis; aa != nil {
		lines = append(lines, fmt.Sprintf(" intelligence %s · coding %s · agentic %s  %s",
			score(aa.Intelligence), score(aa.Coding), score(aa.Agentic), dimStyle.Render("(Artificial Analysis)")))
	} else {
		lines = append(lines, dimStyle.Render(" no benchmark scores yet"))
	}

	limits := " context " + short(info.ContextLength)
	if n := info.TopProvider.MaxCompletionTokens; n > 0 {
		limits += " · max out " + short(n)
	}
	if r := info.Reasoning; r != nil {
		limits += " · reasoning"
		if r.DefaultEffort != "" {
			limits += ": " + r.DefaultEffort
		}
		if len(r.SupportedEfforts) > 0 {
			limits += " (" + strings.Join(r.SupportedEfforts, ", ") + ")"
		}
	} else {
		limits += " · no reasoning"
	}
	lines = append(lines, limits)

	// Two lines of description. ctrl+o opens the page with the rest.
	desc := strings.Split(lipgloss.NewStyle().Width(max(w-2, 10)).Render(oneLine(info.Description)), "\n")
	if len(desc) > 2 {
		desc = desc[:2]
		desc[1] = strings.TrimRight(desc[1], " ") + "…"
	}
	for _, d := range desc {
		lines = append(lines, dimStyle.Render(" "+strings.TrimRight(d, " ")))
	}
	return lipgloss.NewStyle().Height(m.cardRows()).Render(fitLines(strings.Join(lines, "\n"), w))
}

func score(f *float64) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", *f)
}

// short formats a token count: 950, 44k, 200k, 1M, 1.5M.
func short(n int) string {
	switch {
	case n >= 1_000_000:
		// 1,048,576 and 1,000,000 both show as "1M".
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
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
