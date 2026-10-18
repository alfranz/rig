package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alfranz/rig/internal/llm"
	"github.com/alfranz/rig/internal/tools"
)

// MaxCallsPerTurn stops a model that keeps calling tools forever.
const MaxCallsPerTurn = 50

// Session is one conversation with one agent.
type Session struct {
	Agent  *Agent
	Client *llm.Client
	Tools  *tools.Registry

	// Messages is the full conversation. We send all of it on every request.
	Messages []llm.Message

	// Emit receives every event. It must not block for long.
	Emit func(Event)
	// Approve asks the user before a tool with NeedsApproval runs. Nil means always allow.
	Approve func(ctx context.Context, name, args string) bool

	calls int
}

func NewSession(a *Agent, c *llm.Client, reg *tools.Registry, systemPrompt string) *Session {
	return &Session{
		Agent:    a,
		Client:   c,
		Tools:    reg,
		Messages: []llm.Message{{Role: "system", Content: systemPrompt}},
		Emit:     func(Event) {},
	}
}

// Send adds the user message and runs the loop:
// call the model, run the tools it asks for, add the results, and call it again
// until it answers without tool calls.
func (s *Session) Send(ctx context.Context, text string) (err error) {
	defer func() { s.Emit(TurnDone{Err: err}) }()

	s.Messages = append(s.Messages, llm.Message{Role: "user", Content: text})
	for range MaxCallsPerTurn {
		msg, err := s.call(ctx)
		if err != nil {
			return err
		}
		s.Messages = append(s.Messages, msg)
		if len(msg.ToolCalls) == 0 {
			return nil
		}
		// Every tool call needs a tool message with the same id before the next request.
		for _, tc := range msg.ToolCalls {
			s.Messages = append(s.Messages, s.runTool(ctx, tc))
		}
		if err := ctx.Err(); err != nil {
			return err // the user stopped the turn: do not send another request
		}
	}
	return fmt.Errorf("stopped after %d API calls in one turn", MaxCallsPerTurn)
}

// Reset starts a new conversation and keeps the system prompt.
// Do not call it while Send runs: the loop appends to Messages.
func (s *Session) Reset() {
	s.Messages = s.Messages[:1]
	s.Emit(Cleared{At: time.Now()})
}

// call sends one request with the whole conversation and streams the answer.
func (s *Session) call(ctx context.Context) (llm.Message, error) {
	s.calls++
	id := s.calls
	req := llm.Request{
		Model:    s.Agent.Model,
		Messages: s.Messages,
		Tools:    s.Tools.Definitions(),
	}
	res, err := s.Client.Stream(ctx, req, llm.Hooks{
		OnRequest: func(body []byte) { s.Emit(CallStarted{CallID: id, Request: body, At: time.Now()}) },
		OnLine:    func(line string, at time.Duration) { s.Emit(StreamLine{CallID: id, Line: line, Offset: at}) },
		OnText:    func(t string) { s.Emit(TextDelta{CallID: id, Text: t}) },
		OnReasoning: func(t string) {
			s.Emit(ReasoningDelta{CallID: id, Text: t})
		},
	})
	s.Emit(CallFinished{CallID: id, Result: res, Err: err})
	if err != nil {
		return llm.Message{}, err
	}
	return res.Message, nil
}

func (s *Session) runTool(ctx context.Context, tc llm.ToolCall) llm.Message {
	name, args := tc.Function.Name, tc.Function.Arguments
	s.Emit(ToolStarted{CallID: s.calls, ToolCallID: tc.ID, Name: name, Args: args})
	start := time.Now()

	out, err := s.execTool(ctx, name, args)
	if err != nil {
		// The model gets the error as text, so it can fix the call and try again.
		out = "error: " + err.Error()
	}
	if out == "" {
		out = "(no output)" // some providers reject empty tool results
	}
	s.Emit(ToolFinished{ToolCallID: tc.ID, Name: name, Output: out, IsError: err != nil, Duration: time.Since(start)})
	return llm.Message{Role: "tool", ToolCallID: tc.ID, Content: out}
}

func (s *Session) execTool(ctx context.Context, name, args string) (string, error) {
	tool, ok := s.Tools.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if args == "" {
		args = "{}"
	}
	if !json.Valid([]byte(args)) {
		return "", fmt.Errorf("arguments are not valid JSON: %s", args)
	}
	if tool.NeedsApproval && s.Approve != nil && !s.Approve(ctx, name, args) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("the user denied this tool call")
	}
	return tool.Run(ctx, json.RawMessage(args))
}
