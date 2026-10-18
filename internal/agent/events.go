package agent

import (
	"time"

	"github.com/alfranz/rig/internal/llm"
)

// Event is something that happened in the loop. The UI and the log only read events.
type Event interface{ isEvent() }

// CallStarted: we are about to send one HTTP request. Request is the exact JSON body.
type CallStarted struct {
	CallID  int
	Request []byte
	At      time.Time
}

// StreamLine is one raw line of the response body, with the time since the request started.
type StreamLine struct {
	CallID int
	Line   string
	Offset time.Duration
}

type TextDelta struct {
	CallID int
	Text   string
}

type ReasoningDelta struct {
	CallID int
	Text   string
}

// CallFinished: the response is complete. Result can be partial when Err is set.
type CallFinished struct {
	CallID int
	Result *llm.Result
	Err    error
}

// ToolStarted: the model asked for a tool and we are about to run it (after approval).
type ToolStarted struct {
	CallID     int
	ToolCallID string
	Name       string
	Args       string
}

type ToolFinished struct {
	ToolCallID string
	Name       string
	Output     string // what we send back to the model
	IsError    bool
	Duration   time.Duration
}

// TurnDone: the model answered without asking for more tools, or the turn failed.
type TurnDone struct {
	Err error
}

// Cleared: the user started a new conversation. Only the system message is left.
type Cleared struct {
	At time.Time
}

func (CallStarted) isEvent()    {}
func (StreamLine) isEvent()     {}
func (TextDelta) isEvent()      {}
func (ReasoningDelta) isEvent() {}
func (CallFinished) isEvent()   {}
func (ToolStarted) isEvent()    {}
func (ToolFinished) isEvent()   {}
func (TurnDone) isEvent()       {}
func (Cleared) isEvent()        {}
