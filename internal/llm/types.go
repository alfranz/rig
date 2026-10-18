// Package llm talks to the OpenRouter chat completions API with plain net/http.
// The types mirror the OpenAI-compatible JSON format that OpenRouter uses.
package llm

import (
	"encoding/json"
	"time"
)

// Message is one entry in the conversation that we send on every request.
type Message struct {
	Role string `json:"role"` // system, user, assistant, tool
	// Content is omitted when empty: an assistant message that only calls tools has no text.
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool only: the id of the call it answers
}

// ToolCall is the model asking us to run a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON object encoded as a string. The model writes it, so it can be invalid.
	Arguments string `json:"arguments"`
}

// Tool is a tool definition that we send in the "tools" array.
type Tool struct {
	Type     string      `json:"type"` // always "function"
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Request is the body of POST /chat/completions.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	// Usage asks OpenRouter to put token counts and cost in the last stream chunk.
	Usage *UsageOption `json:"usage,omitempty"`
}

type UsageOption struct {
	Include bool `json:"include"`
}

// Chunk is one "data: {...}" line of the stream.
type Chunk struct {
	ID       string        `json:"id"`
	Model    string        `json:"model"`
	Provider string        `json:"provider"`
	Choices  []ChunkChoice `json:"choices"`
	Usage    *Usage        `json:"usage"`
	Error    *APIError     `json:"error"`
}

type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta is the new part of the assistant message in one chunk.
type Delta struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Reasoning string          `json:"reasoning"` // some models stream their thinking here
	ToolCalls []ToolCallDelta `json:"tool_calls"`
}

// ToolCallDelta is one piece of a tool call. The first piece for an index has the id
// and the name. Later pieces only add more text to Arguments.
type ToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Usage struct {
	PromptTokens            int                `json:"prompt_tokens"`
	CompletionTokens        int                `json:"completion_tokens"`
	TotalTokens             int                `json:"total_tokens"`
	Cost                    float64            `json:"cost"` // US dollars
	PromptTokensDetails     *PromptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionDetails `json:"completion_tokens_details,omitempty"`
}

type PromptDetails struct {
	CachedTokens     int `json:"cached_tokens"`      // read from the prompt cache
	CacheWriteTokens int `json:"cache_write_tokens"` // written to the prompt cache
}

type CompletionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type APIError struct {
	Code     any            `json:"code"` // OpenRouter sends a number, some providers a string
	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Result is one finished API call: the assembled message plus everything we measured.
type Result struct {
	Status       int // HTTP status code
	ID           string
	Model        string
	Provider     string
	Message      Message
	Reasoning    string
	FinishReason string
	Usage        *Usage
	Started      time.Time
	TTFT         time.Duration // time to first token (text or tool call)
	Duration     time.Duration
}
