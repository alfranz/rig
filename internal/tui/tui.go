// Package tui is the terminal UI: a chat pane, a list of API calls, and a detail view
// that shows the raw request, the raw stream, and the result of each call.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alfranz/rig/internal/agent"
	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/state"
)

// Options are the settings from main.
type Options struct {
	LogPath   string
	Yolo      bool
	StatePath string      // ~/.rig/state.json
	State     state.State // as loaded at startup; /model updates it
}

// Run starts the TUI. It wraps sess.Emit, so events go to the log and to the UI.
func Run(sess *agent.Session, opts Options) error {
	m := newModel(sess, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	logEvent := sess.Emit
	sess.Emit = func(e agent.Event) {
		logEvent(e)
		p.Send(e)
	}
	sess.Approve = nil
	if !opts.Yolo {
		// The agent goroutine waits here until the user presses y or n in the UI.
		sess.Approve = func(ctx context.Context, name, args string) bool {
			reply := make(chan bool, 1)
			p.Send(approvalMsg{name: name, args: args, reply: reply})
			select {
			case ok := <-reply:
				return ok
			case <-ctx.Done():
				return false
			}
		}
	}
	_, err := p.Run()
	return err
}

type approvalMsg struct {
	name, args string
	reply      chan bool
}

// modelsMsg is the answer to GET /models.
type modelsMsg struct {
	models []llm.ModelInfo
	err    error
}

// errMsg is an error from a command that ran outside Update.
type errMsg struct{ err error }

type entryKind int

const (
	kindInfo entryKind = iota
	kindUser
	kindAssistant
	kindReasoning
	kindTool
	kindToolResult
	kindToolError
	kindError
)

// entry is one block in the chat pane.
type entry struct {
	kind entryKind
	text string
}

// callView is everything we know about one API call.
type callView struct {
	id      int
	started time.Time
	request []byte
	lines   []agent.StreamLine
	res     *llm.Result
	err     error
	done    bool
}

type tab int

const (
	tabRequest tab = iota
	tabStream
	tabResult
)

var tabNames = []string{"Request", "Stream", "Result"}

type model struct {
	sess *agent.Session
	opts Options
	// modelID is our copy of sess.Agent.Model. The UI reads it, and only ModelChanged writes it,
	// so View never reads a field that a command goroutine writes.
	modelID string

	width, height int
	ready         bool

	entries      []entry
	curText      int // index of the assistant entry that is streaming, or -1
	curReasoning int
	chat         viewport.Model
	input        textinput.Model
	menuSel      int // selected row in the slash command menu
	menuRows     int // rows the menu uses now; the chat pane gets shorter by this much

	calls      []*callView
	sel        int  // selected call
	follow     bool // select each new call as it starts
	callsTop   int  // first visible row in the calls pane
	focusCalls bool

	detail    bool
	detailTab tab
	detailVP  viewport.Model

	// Full screen views. At most one is open. The detail view above is the third one.
	picker     *picker
	page       string             // title of the /context or /usage view, "" when closed
	pageRender func(w int) string // builds the page again when the width changes
	pageVP     viewport.Model

	models        []llm.ModelInfo // from GET /models, nil until it arrives
	modelsErr     error
	modelsLoading bool
	used          map[string]int // calls per model id, for the "used" column and sort

	busy       bool
	cancelTurn context.CancelFunc
	approval   *approvalMsg

	totalIn, totalOut int
	totalCost         float64
	ctxTokens         int // context size after the last call, from its usage block
}

func newModel(sess *agent.Session, opts Options) *model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "Ask the agent. Enter sends."
	in.CharLimit = 0
	in.Focus()

	a := sess.Agent
	m := &model{
		sess: sess, opts: opts, modelID: sess.Agent.Model,
		input: in, chat: viewport.New(0, 0), detailVP: viewport.New(0, 0), pageVP: viewport.New(0, 0),
		curText: -1, curReasoning: -1, sel: -1, follow: true, used: map[string]int{},
	}
	m.chat.MouseWheelEnabled = true
	m.detailVP.MouseWheelEnabled = true
	m.pageVP.MouseWheelEnabled = true
	m.entries = []entry{
		{kindInfo, fmt.Sprintf("agent %s (%s) · model %s", a.Name, a.Source, a.Model)},
		{kindInfo, fmt.Sprintf("tools: %s", strings.Join(a.Tools, ", "))},
		{kindInfo, "project instructions: " + instructionsLabel()},
		{kindInfo, "log: " + opts.LogPath},
		{kindInfo, "/: commands · tab: switch to the API calls pane · click a call to inspect it · esc: stop the agent · ctrl+c: quit"},
	}
	if opts.Yolo {
		m.entries = append(m.entries, entry{kindInfo, "--yolo: bash and edit_file run without asking"})
	}
	return m
}

func instructionsLabel() string {
	wd, _ := os.Getwd()
	if name, _, ok := agent.ProjectInstructions(wd); ok {
		return name
	}
	return "none (no AGENTS.md or CLAUDE.md)"
}

// Init loads the model list and the usage counts in the background, so /model and the context limit are ready when needed.
func (m *model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.fetchModels(), m.loadUsed()) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = msg.Width, msg.Height, true
		m.layout()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case approvalMsg:
		// The prompt is in the main view, so close any full screen view.
		m.approval = &msg
		m.detail, m.picker, m.page = false, nil, ""
		m.input.Focus()
		return m, nil
	case modelsMsg:
		m.models, m.modelsErr, m.modelsLoading = msg.models, msg.err, false
		if m.picker != nil {
			m.picker.refresh(m)
		}
		return m, nil
	case usedMsg:
		for id, n := range msg.counts {
			m.used[id] += n
		}
		return m, nil
	case usageMsg:
		m.openUsage(msg)
		return m, nil
	case errMsg:
		m.entries = append(m.entries, entry{kindError, msg.err.Error()})
		m.refreshChat()
		return m, nil
	case agent.Event:
		m.handleEvent(msg)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg) // cursor blink
	return m, cmd
}

func (m *model) handleEvent(e agent.Event) {
	switch e := e.(type) {
	case agent.CallStarted:
		m.calls = append(m.calls, &callView{id: e.CallID, started: e.At, request: e.Request})
		m.curText, m.curReasoning = -1, -1
		if m.follow {
			m.sel = len(m.calls) - 1
			m.keepSelVisible()
		}
	case agent.StreamLine:
		if c := m.call(e.CallID); c != nil {
			c.lines = append(c.lines, e)
		}
	case agent.TextDelta:
		m.curText = m.appendStreaming(m.curText, kindAssistant, e.Text)
	case agent.ReasoningDelta:
		m.curReasoning = m.appendStreaming(m.curReasoning, kindReasoning, e.Text)
	case agent.CallFinished:
		if c := m.call(e.CallID); c != nil {
			c.res, c.err, c.done = e.Result, e.Err, true
		}
		m.used[m.modelID]++
		if e.Result != nil && e.Result.Usage != nil {
			u := e.Result.Usage
			m.totalIn += u.PromptTokens
			m.totalOut += u.CompletionTokens
			m.totalCost += u.Cost
			// The answer joins the context too. Reasoning does not: we do not send it back.
			m.ctxTokens = u.PromptTokens + u.CompletionTokens
			if d := u.CompletionTokensDetails; d != nil {
				m.ctxTokens -= d.ReasoningTokens
			}
		}
	case agent.ToolStarted:
		m.entries = append(m.entries, entry{kindTool, e.Name + " " + e.Args})
	case agent.ToolFinished:
		kind := kindToolResult
		if e.IsError {
			kind = kindToolError
		}
		m.entries = append(m.entries, entry{kind, summarize(e.Output, e.Duration)})
	case agent.Cleared:
		m.busy = false
		m.entries = []entry{{kindInfo, "── context cleared · the next message starts a new conversation ──"}}
		m.curText, m.curReasoning = -1, -1
		m.ctxTokens = 0
	case agent.ModelChanged:
		m.busy = false
		m.modelID = e.To
		m.entries = append(m.entries, entry{kindInfo, fmt.Sprintf("model: %s → %s", e.From, e.To)})
	case agent.TurnDone:
		m.busy, m.cancelTurn, m.approval = false, nil, nil
		switch {
		case errors.Is(e.Err, context.Canceled):
			m.entries = append(m.entries, entry{kindInfo, "stopped"})
		case e.Err != nil:
			m.entries = append(m.entries, entry{kindError, e.Err.Error()})
		}
	}
	m.refreshChat()
	if m.detail {
		m.refreshDetail(false)
	}
}

// appendStreaming adds text to the streaming entry at idx, or starts a new one.
func (m *model) appendStreaming(idx int, kind entryKind, text string) int {
	if idx < 0 {
		m.entries = append(m.entries, entry{kind: kind})
		idx = len(m.entries) - 1
	}
	m.entries[idx].text += text
	return idx
}

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		if m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, tea.Quit
	}

	if m.approval != nil {
		switch k.String() {
		case "y", "Y":
			m.approval.reply <- true
			m.approval = nil
		case "n", "N", "esc":
			m.approval.reply <- false
			m.approval = nil
		}
		return m, nil
	}

	if m.picker != nil {
		return m, m.pickerKey(k)
	}
	if m.page != "" {
		switch k.String() {
		case "esc", "q":
			m.page = ""
			return m, nil
		}
		var cmd tea.Cmd
		m.pageVP, cmd = m.pageVP.Update(k)
		return m, cmd
	}

	if m.detail {
		switch k.String() {
		case "esc", "q":
			m.detail = false
		case "tab", "right", "l":
			m.detailTab = (m.detailTab + 1) % 3
			m.refreshDetail(true)
		case "shift+tab", "left", "h":
			m.detailTab = (m.detailTab + 2) % 3
			m.refreshDetail(true)
		case "1", "2", "3":
			m.detailTab = tab(k.String()[0] - '1')
			m.refreshDetail(true)
		default:
			var cmd tea.Cmd
			m.detailVP, cmd = m.detailVP.Update(k)
			return m, cmd
		}
		return m, nil
	}

	if matches := m.menuMatches(); len(matches) > 0 && !m.focusCalls {
		n := len(matches)
		switch k.String() {
		case "up":
			m.menuSel = (m.menuSel + n - 1) % n
			return m, nil
		case "down":
			m.menuSel = (m.menuSel + 1) % n
			return m, nil
		case "tab":
			m.input.SetValue("/" + matches[m.menuSel].name)
			m.input.CursorEnd()
			m.afterInput()
			return m, nil
		case "esc":
			m.input.Reset()
			m.afterInput()
			return m, nil
		case "enter":
			m.input.Reset()
			m.afterInput()
			return m, matches[m.menuSel].run(m)
		}
	}

	switch k.String() {
	case "tab":
		m.setFocusCalls(!m.focusCalls)
		return m, nil
	case "esc":
		if m.cancelTurn != nil {
			m.cancelTurn()
		} else if m.focusCalls {
			m.setFocusCalls(false)
		}
		return m, nil
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(k)
		return m, cmd
	}

	if m.focusCalls {
		switch k.String() {
		case "up", "k":
			m.moveSel(-1)
		case "down", "j":
			m.moveSel(1)
		case "enter":
			m.openDetail()
		}
		return m, nil
	}

	if k.Type == tea.KeyEnter {
		return m, m.submit()
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != before {
		m.menuSel = 0
	}
	m.afterInput()
	return m, cmd
}

// afterInput colors a command in the input and resizes the panes when the menu grows or shrinks.
func (m *model) afterInput() {
	n := len(m.menuMatches())
	m.menuSel = min(m.menuSel, max(n-1, 0))
	m.input.TextStyle = lipgloss.NewStyle()
	if strings.HasPrefix(m.input.Value(), "/") {
		m.input.TextStyle = cmdStyle
	}
	if n != m.menuRows {
		m.menuRows = n
		m.layout()
	}
}

func (m *model) handleMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.picker != nil {
		if ev.Button == tea.MouseButtonWheelUp {
			m.picker.move(-1, m.pickerRows())
		} else if ev.Button == tea.MouseButtonWheelDown {
			m.picker.move(1, m.pickerRows())
		}
		return m, nil
	}
	if m.page != "" {
		var cmd tea.Cmd
		m.pageVP, cmd = m.pageVP.Update(ev)
		return m, cmd
	}
	if m.detail {
		var cmd tea.Cmd
		m.detailVP, cmd = m.detailVP.Update(ev)
		return m, cmd
	}
	inCalls := ev.X >= m.chatWidth() && ev.Y < m.topHeight()
	if tea.MouseEvent(ev).IsWheel() {
		if inCalls {
			if ev.Button == tea.MouseButtonWheelUp {
				m.moveSel(-1)
			} else if ev.Button == tea.MouseButtonWheelDown {
				m.moveSel(1)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(ev)
		return m, cmd
	}
	if ev.Action != tea.MouseActionPress || ev.Button != tea.MouseButtonLeft {
		return m, nil
	}
	if !inCalls {
		m.setFocusCalls(false)
		return m, nil
	}
	// Rows start below the top border and the title line.
	row := ev.Y - 2
	if idx := m.callsTop + row; row >= 0 && row < m.callRows() && idx < len(m.calls) {
		m.sel = idx
		m.follow = idx == len(m.calls)-1
		m.setFocusCalls(true)
		m.openDetail()
	}
	return m, nil
}

func (m *model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if strings.HasPrefix(text, "/") {
		m.input.Reset()
		m.afterInput()
		return m.runCommand(text) // commands also work while the agent is busy
	}
	if text == "" || m.busy {
		return nil
	}
	m.input.Reset()
	m.entries = append(m.entries, entry{kindUser, text})
	m.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.refreshChat()
	m.chat.GotoBottom()
	sess := m.sess
	// Bubble Tea runs commands in their own goroutine. Send emits events back to the UI.
	return func() tea.Msg {
		sess.Send(ctx, text)
		return nil
	}
}

func (m *model) setFocusCalls(on bool) {
	m.focusCalls = on
	if on {
		m.input.Blur()
		if m.sel < 0 && len(m.calls) > 0 {
			m.sel = len(m.calls) - 1
		}
	} else {
		m.input.Focus()
	}
}

func (m *model) moveSel(d int) {
	if len(m.calls) == 0 {
		return
	}
	m.sel = min(max(m.sel+d, 0), len(m.calls)-1)
	m.follow = m.sel == len(m.calls)-1
	m.keepSelVisible()
}

func (m *model) keepSelVisible() {
	if m.sel < 0 {
		return
	}
	rows := m.callRows()
	if m.sel < m.callsTop {
		m.callsTop = m.sel
	}
	if rows > 0 && m.sel >= m.callsTop+rows {
		m.callsTop = m.sel - rows + 1
	}
}

func (m *model) openDetail() {
	if m.sel < 0 || m.sel >= len(m.calls) {
		return
	}
	m.detail = true
	m.refreshDetail(true)
}

func (m *model) call(id int) *callView {
	for i := len(m.calls) - 1; i >= 0; i-- {
		if m.calls[i].id == id {
			return m.calls[i]
		}
	}
	return nil
}

// Layout: chat pane (55% of the width) and calls pane on top, then the command menu, the input box and the status bar.
func (m *model) chatWidth() int { return m.width * 55 / 100 }
func (m *model) topHeight() int { return max(m.height-4-m.menuRows, 5) } // input box is 3 rows, status bar 1
func (m *model) callRows() int  { return m.topHeight() - 3 }             // minus border and title

func (m *model) layout() {
	m.chat.Width = max(m.chatWidth()-4, 10)
	m.chat.Height = max(m.topHeight()-3, 1)
	m.input.Width = max(m.width-8, 10)
	m.detailVP.Width = m.width
	m.detailVP.Height = max(m.height-3, 1)
	m.pageVP.Width = m.width
	m.pageVP.Height = max(m.height-3, 1)
	if m.page != "" {
		m.pageVP.SetContent(m.pageRender(m.width))
	}
	if m.picker != nil {
		m.picker.filter.Width = max(m.width-6, 10)
		m.picker.keepVisible(m.pickerRows())
	}
	m.keepSelVisible()
	m.refreshChat()
	if m.detail {
		m.refreshDetail(false)
	}
}

func (m *model) refreshChat() {
	atBottom := m.chat.AtBottom()
	m.chat.SetContent(m.renderEntries(m.chat.Width))
	if atBottom {
		m.chat.GotoBottom()
	}
}

// refreshDetail rebuilds the detail view. On the Stream tab it follows new lines if you are at the bottom.
func (m *model) refreshDetail(top bool) {
	if m.sel < 0 || m.sel >= len(m.calls) {
		return
	}
	atBottom := m.detailVP.AtBottom()
	m.detailVP.SetContent(wrap(m.detailContent(m.calls[m.sel]), m.detailVP.Width))
	switch {
	case top:
		m.detailVP.GotoTop()
	case atBottom && m.detailTab == tabStream:
		m.detailVP.GotoBottom()
	}
}
