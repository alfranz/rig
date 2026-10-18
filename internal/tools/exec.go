package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const bashTimeout = 60 * time.Second

func Bash() *Tool {
	return &Tool{
		Name: "bash",
		Description: "Run a shell command with bash in the working directory. Returns stdout and stderr together and the exit code. " +
			"Use it to build, run tests, and use git. Commands time out after 60 seconds. Do not start servers or other commands that never end.",
		Schema: `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The command to run."}
  },
  "required": ["command"]
}`,
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Command string `json:"command"`
			}
			if err := decode(raw, &a); err != nil {
				return "", err
			}
			if a.Command == "" {
				return "", errors.New("command is required")
			}
			out, code, err := run(ctx, bashTimeout, "bash", "-c", a.Command)
			if err != nil {
				return "", err
			}
			if out == "" {
				out = "(no output)"
			}
			return truncate(fmt.Sprintf("%s\n[exit code %d]", out, code)), nil
		},
	}
}

func CodeSearch() *Tool {
	return &Tool{
		Name: "code_search",
		Description: "Search file contents with ripgrep (rg). The pattern is a regular expression. " +
			"Returns matching lines as path:line:text. Use this to find where something is defined or used.",
		Schema: `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for."},
    "path":    {"type": "string", "description": "File or directory to search. Default is the working directory."},
    "glob":    {"type": "string", "description": "Only search files that match this glob, for example '*.go'."}
  },
  "required": ["pattern"]
}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Glob    string `json:"glob"`
			}
			if err := decode(raw, &a); err != nil {
				return "", err
			}
			if a.Pattern == "" {
				return "", errors.New("pattern is required")
			}
			args := []string{"--line-number", "--no-heading", "--color=never", "--max-columns=300"}
			if a.Glob != "" {
				args = append(args, "--glob", a.Glob)
			}
			args = append(args, "-e", a.Pattern)
			if a.Path != "" {
				args = append(args, a.Path)
			}
			out, code, err := run(ctx, bashTimeout, "rg", args...)
			if err != nil {
				return "", err
			}
			switch code {
			case 0:
				return truncate(out), nil
			case 1:
				return "no matches", nil
			default:
				return "", fmt.Errorf("rg failed (exit %d): %s", code, out)
			}
		},
	}
}

// run starts a command in its own process group, so a timeout also kills its children.
// It returns the combined output and the exit code.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second // do not hang if a child keeps the output pipe open
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return buf.String(), -1, fmt.Errorf("timed out after %s; output so far:\n%s", timeout, buf.String())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return buf.String(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return "", -1, err
	}
	return buf.String(), 0, nil
}
