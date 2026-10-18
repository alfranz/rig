// Package agent loads agent definitions and runs the agent loop.
package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Agent is one agent file: a YAML header plus the system prompt.
type Agent struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Tools       []string `yaml:"tools"`
	Prompt      string   `yaml:"-"` // the Markdown body after the header
	Source      string   `yaml:"-"` // where the file came from
}

// Load finds <name>.md in the first directory that has it.
// If no directory has it, it uses the built-in copy from the binary.
func Load(name string, dirs []string, builtin fs.FS) (*Agent, error) {
	file := name + ".md"
	for _, dir := range dirs {
		p := filepath.Join(dir, file)
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return Parse(data, p)
	}
	data, err := fs.ReadFile(builtin, ".rig/agents/"+file)
	if err != nil {
		return nil, fmt.Errorf("agent %q not found in %v or built in", name, dirs)
	}
	return Parse(data, "built-in")
}

// Parse reads an agent file: "---\n<yaml>\n---\n<prompt>".
func Parse(data []byte, source string) (*Agent, error) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return nil, fmt.Errorf("%s: file must start with a --- header", source)
	}
	header, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return nil, fmt.Errorf("%s: header has no closing ---", source)
	}
	var a Agent
	if err := yaml.Unmarshal(header, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if a.Name == "" || a.Model == "" {
		return nil, fmt.Errorf("%s: name and model are required", source)
	}
	a.Prompt = strings.TrimSpace(string(body))
	a.Source = source
	return &a, nil
}

// SystemPrompt is the agent prompt plus facts about the environment.
func (a *Agent) SystemPrompt(workDir string, now time.Time) string {
	var b strings.Builder
	b.WriteString(a.Prompt)
	fmt.Fprintf(&b, "\n\n<env>\nos: %s/%s\nworking directory: %s\ndate: %s\n</env>",
		runtime.GOOS, runtime.GOARCH, workDir, now.Format("2006-01-02"))
	if name, data, ok := ProjectInstructions(workDir); ok {
		fmt.Fprintf(&b, "\n\n<project_instructions file=%q>\n%s\n</project_instructions>", name, strings.TrimSpace(string(data)))
	}
	return b.String()
}

// instructionFiles are tried in order. Only the first one found is used.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// ProjectInstructions reads AGENTS.md from workDir, or CLAUDE.md when there is no AGENTS.md.
func ProjectInstructions(workDir string) (name string, data []byte, ok bool) {
	for _, name := range instructionFiles {
		if data, err := os.ReadFile(filepath.Join(workDir, name)); err == nil {
			return name, data, true
		}
	}
	return "", nil, false
}
