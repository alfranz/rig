// Command rig is a small coding agent that talks to OpenRouter directly.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/alfranz/rig"
	"github.com/alfranz/rig/internal/agent"
	"github.com/alfranz/rig/internal/jsonl"
	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/tools"
	"github.com/alfranz/rig/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rig:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		agentName = flag.String("agent", "general", "agent to use: <name>.md in .rig/agents or ~/.rig/agents")
		model     = flag.String("model", "", "override the model from the agent file")
		raw       = flag.String("raw", "", "send one request with this prompt, print every raw stream line, then exit")
		prompt    = flag.String("p", "", "run one turn with this prompt in plain text mode (no TUI), then exit")
		yolo      = flag.Bool("yolo", false, "run bash and edit_file without asking")
		logDir    = flag.String("log-dir", filepath.Join(home, ".rig", "logs"), "directory for JSONL logs")
	)
	flag.Parse()

	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return fmt.Errorf("OPENROUTER_API_KEY is not set")
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	a, err := agent.Load(*agentName, []string{
		filepath.Join(workDir, ".rig", "agents"),
		filepath.Join(home, ".rig", "agents"),
	}, rig.BuiltinAgents)
	if err != nil {
		return err
	}
	if *model != "" {
		a.Model = *model
	}
	reg, err := tools.Default().Subset(a.Tools)
	if err != nil {
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}

	client := llm.NewClient(key)
	sess := agent.NewSession(a, client, reg, a.SystemPrompt(workDir, time.Now()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *raw != "" {
		return runRaw(ctx, client, sess, *raw)
	}

	logger, err := jsonl.Open(*logDir)
	if err != nil {
		return err
	}
	defer logger.Close()
	sess.Emit = logger.Handle

	if *prompt != "" {
		if !*yolo {
			sess.Approve = askStdin
		}
		return runPlain(ctx, sess, *prompt, logger.Path)
	}
	return tui.Run(sess, logger.Path, *yolo)
}

// runRaw sends exactly one request and prints the wire traffic. Good for learning the format.
func runRaw(ctx context.Context, client *llm.Client, sess *agent.Session, text string) error {
	req := llm.Request{
		Model:    sess.Agent.Model,
		Messages: append(sess.Messages, llm.Message{Role: "user", Content: text}),
		Tools:    sess.Tools.Definitions(),
	}
	res, err := client.Stream(ctx, req, llm.Hooks{
		OnRequest: func(body []byte) {
			fmt.Printf("=== request: POST %s/chat/completions (%d bytes) ===\n", client.BaseURL, len(body))
			fmt.Println(prettyJSON(body))
			fmt.Println("\n=== response stream ===")
		},
		OnLine: func(line string, at time.Duration) {
			fmt.Printf("+%6.3fs  %s\n", at.Seconds(), line)
		},
	})
	if res != nil {
		fmt.Println("\n=== assembled result ===")
		fmt.Printf("status: %d  finish_reason: %s  model: %s  provider: %s\n", res.Status, res.FinishReason, res.Model, res.Provider)
		fmt.Printf("time to first token: %.2fs  total: %.2fs\n", res.TTFT.Seconds(), res.Duration.Seconds())
		if res.Usage != nil {
			fmt.Printf("tokens: in %d  out %d  total %d  cost $%.6f\n",
				res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens, res.Usage.Cost)
		}
		msg, _ := json.MarshalIndent(res.Message, "", "  ")
		fmt.Printf("message:\n%s\n", msg)
	}
	return err
}

// runPlain runs one turn and prints the events as plain text.
func runPlain(ctx context.Context, sess *agent.Session, text, logPath string) error {
	logEvent := sess.Emit
	var in, out int
	var cost float64
	sess.Emit = func(e agent.Event) {
		logEvent(e)
		switch e := e.(type) {
		case agent.CallStarted:
			fmt.Printf("\n[call #%d → %d bytes, %d messages]\n", e.CallID, len(e.Request), len(sess.Messages))
		case agent.TextDelta:
			fmt.Print(e.Text)
		case agent.CallFinished:
			if r := e.Result; r != nil {
				fmt.Printf("\n[call #%d ← %d %s in %.1fs", e.CallID, r.Status, r.FinishReason, r.Duration.Seconds())
				if u := r.Usage; u != nil {
					in, out, cost = in+u.PromptTokens, out+u.CompletionTokens, cost+u.Cost
					fmt.Printf(", tokens in %d out %d, $%.4f", u.PromptTokens, u.CompletionTokens, u.Cost)
				}
				fmt.Println("]")
			}
		case agent.ToolStarted:
			fmt.Printf("⚙ %s %s\n", e.Name, e.Args)
		case agent.ToolFinished:
			first, _, _ := strings.Cut(e.Output, "\n")
			fmt.Printf("  ↳ %s (%d bytes)\n", first, len(e.Output))
		}
	}
	err := sess.Send(ctx, text)
	fmt.Printf("\n[session: tokens in %d out %d, $%.4f · log: %s]\n", in, out, cost, logPath)
	return err
}

func askStdin(ctx context.Context, name, args string) bool {
	fmt.Fprintf(os.Stderr, "allow %s %s? [y/N] ", name, args)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "y"
}

// prettyJSON indents JSON and keeps the key order, so it matches what went over the wire.
func prettyJSON(b []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return string(b)
	}
	return out.String()
}
