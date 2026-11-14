package event

import "github.com/keyute/kopi/internal/policy"

type Event interface{ isEvent() }

type TextDelta struct{ Text string }

type ThinkingDelta struct{ Text string }

// ToolStart carries the call's JSON arguments, compacted.
type ToolStart struct{ CallID, Name, Args string }

type ToolEnd struct {
	CallID, Name, Output string
	IsError              bool
}

// Notice reports retry and compaction progress.
type Notice struct{ Text string }

type TurnEnd struct {
	WorkID  string
	Outcome Outcome
}

type ModeChanged struct{ Mode policy.Mode }

type SessionChanged struct {
	SessionID    string
	Resumed      bool
	MessageCount int
}

type Error struct{ Err error }

func (TextDelta) isEvent()      {}
func (ThinkingDelta) isEvent()  {}
func (ToolStart) isEvent()      {}
func (ToolEnd) isEvent()        {}
func (Notice) isEvent()         {}
func (TurnEnd) isEvent()        {}
func (ModeChanged) isEvent()    {}
func (SessionChanged) isEvent() {}
func (Error) isEvent()          {}

type Status int

const (
	Completed Status = iota
	Interrupted
	Failed
)

func (s Status) String() string {
	switch s {
	case Completed:
		return "completed"
	case Interrupted:
		return "interrupted"
	case Failed:
		return "failed"
	}
	return "unknown"
}

type Outcome struct {
	Status Status
	// Message is the provider or tool error text of a Failed turn.
	Message string
}
