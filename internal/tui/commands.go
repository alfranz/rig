package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// command is one slash command. Commands act on the UI, so they live here and not in the agent.
type command struct {
	name    string
	aliases []string
	help    string
	run     func(m *model) tea.Cmd
}

var commands = []command{
	{name: "clear", help: "start a new conversation, keep the system prompt", run: (*model).cmdClear},
	{name: "exit", aliases: []string{"quit"}, help: "quit rig", run: (*model).cmdExit},
}

func findCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name || slices.Contains(c.aliases, name) {
			return c, true
		}
	}
	return command{}, false
}

// menuMatches is the list for the menu above the input. It is empty unless the input is "/" plus part of a name.
func (m *model) menuMatches() []command {
	prefix, ok := strings.CutPrefix(m.input.Value(), "/")
	if !ok || strings.Contains(prefix, " ") {
		return nil
	}
	var out []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, prefix) || hasPrefixAny(c.aliases, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// runCommand runs "/name". An unknown name is an error in the chat, so a typo never goes to the model.
func (m *model) runCommand(text string) tea.Cmd {
	name, _, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	c, ok := findCommand(name)
	if !ok {
		m.entries = append(m.entries, entry{kindError, "unknown command /" + name + " · type / to see the commands"})
		m.refreshChat()
		return nil
	}
	return c.run(m)
}

func (m *model) cmdClear() tea.Cmd {
	if m.busy {
		m.entries = append(m.entries, entry{kindError, "/clear: the agent is working · press esc first"})
		m.refreshChat()
		return nil
	}
	// Reset emits an event, and the program cannot take a message while Update runs. So run it as a command.
	// busy stays on until the Cleared event arrives, so no turn can start in between.
	m.busy = true
	sess := m.sess
	return func() tea.Msg {
		sess.Reset()
		return nil
	}
}

func (m *model) cmdExit() tea.Cmd {
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	return tea.Quit
}

func (m *model) viewMenu() string {
	var rows []string
	for i, c := range m.menuMatches() {
		name := "/" + c.name
		help := c.help
		if len(c.aliases) > 0 {
			help += " (also /" + strings.Join(c.aliases, ", /") + ")"
		}
		plain := ansi.Truncate(" "+padRight(name, 10)+" "+help, m.width, "…")
		if i == m.menuSel {
			rows = append(rows, selStyle.Render(padRight(plain, m.width)))
			continue
		}
		// Style after truncating, so the width is measured on plain text.
		w := ansi.StringWidth(" " + padRight(name, 10))
		rows = append(rows, cmdStyle.Render(ansi.Truncate(plain, w, ""))+dimStyle.Render(ansi.Cut(plain, w, m.width)))
	}
	return strings.Join(rows, "\n")
}

func padRight(s string, w int) string { return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0)) }

func hasPrefixAny(list []string, prefix string) bool {
	for _, x := range list {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}
