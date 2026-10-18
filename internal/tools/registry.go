// Package tools holds the tools the model can call.
// Each tool has a hand-written JSON Schema: this is exactly what the model sees.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alfranz/rig/internal/llm"
)

// MaxOutput is the most bytes a tool result can send back to the model.
const MaxOutput = 30_000

type Tool struct {
	Name        string
	Description string // a prompt: it tells the model when and how to use the tool
	Schema      string // JSON Schema for the arguments
	// NeedsApproval tools change things on disk or run commands. The user must say yes first.
	NeedsApproval bool
	Run           func(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	tools map[string]*Tool
	order []string
}

func NewRegistry(tools ...*Tool) *Registry {
	r := &Registry{tools: map[string]*Tool{}}
	for _, t := range tools {
		if !json.Valid([]byte(t.Schema)) {
			panic("tools: invalid JSON Schema for " + t.Name)
		}
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	return r
}

// Default returns all built-in tools.
func Default() *Registry {
	return NewRegistry(ReadFile(), ListFiles(), EditFile(), Bash(), CodeSearch())
}

func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Subset returns a registry with only the named tools, in the given order.
func (r *Registry) Subset(names []string) (*Registry, error) {
	var picked []*Tool
	for _, n := range names {
		t, ok := r.tools[n]
		if !ok {
			return nil, fmt.Errorf("unknown tool %q (known: %v)", n, r.order)
		}
		picked = append(picked, t)
	}
	return NewRegistry(picked...), nil
}

// Definitions returns the "tools" array for the API request.
func (r *Registry) Definitions() []llm.Tool {
	defs := make([]llm.Tool, 0, len(r.order))
	for _, n := range r.order {
		t := r.tools[n]
		defs = append(defs, llm.Tool{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(t.Schema),
			},
		})
	}
	return defs
}

// truncate cuts s to MaxOutput bytes and says so, so the model knows output is missing.
func truncate(s string) string {
	if len(s) <= MaxOutput {
		return s
	}
	return s[:MaxOutput] + fmt.Sprintf("\n\n[output cut: showed %d of %d bytes]", MaxOutput, len(s))
}

// decode parses the model's arguments into v and gives a clear error the model can act on.
func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments JSON: %v", err)
	}
	return nil
}
