package pigruntime

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"

	"github.com/keyute/kopi/internal/event"
)

// Session wraps a PiG session. PiG's Session.Events and Session.ListSessions
// are never used: an un-drained Events channel stalls the agent loop, and
// ListSessions reads ~/.pig.
type Session struct {
	inner *coding.Session
}

func (s *Session) ID() string { return s.inner.ID() }

// MessageCount counts user, assistant and tool-result messages; PiG also keeps
// the system prompt and tool set as system messages in the transcript.
func (s *Session) MessageCount() int {
	n := 0
	for _, m := range s.inner.Messages() {
		if m.User != nil || m.Assistant != nil || m.ToolResult != nil {
			n++
		}
	}
	return n
}

// Subscribe registers fn to run synchronously on PiG's event executor; a slow
// fn stalls the turn. Every call for a turn has run when Send returns.
func (s *Session) Subscribe(fn func(event.Event)) func() {
	return s.inner.Subscribe(func(ev agent.AgentEvent) {
		if e, ok := translate(ev); ok {
			fn(e)
		}
	})
}

// Send runs one turn. Cancelling ctx is the abort request for this turn
// only: a ctx already done returns Interrupted without calling the provider,
// and a turn Kopi aborted reports Interrupted unless its last assistant message
// stopped normally (PiG reports an abort during a tool as StopReason "error"
// with "context canceled"). Model failures are not
// errors: they surface as a Failed outcome carrying the provider's error text.
func (s *Session) Send(ctx context.Context, text string) (event.Outcome, error) {
	if ctx.Err() != nil {
		return event.Outcome{Status: event.Interrupted}, nil
	}
	var aborted atomic.Bool
	abort := func() {
		aborted.Store(true)
		s.inner.RequestAbort()
	}
	unsubscribe := s.inner.Subscribe(abortAtRunStart(ctx, abort))
	defer unsubscribe()
	abortDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(abortDone)
		abort()
	})
	// PiG gets an uncancellable ctx: a cancelled one makes it return
	// ctx.Err() instead of the turn, so RequestAbort is the only abort path.
	msgs, err := s.inner.Send(context.WithoutCancel(ctx), text)
	// An abort already under way must finish before Send returns, or its
	// RequestAbort could land on the following turn.
	if !stop() {
		<-abortDone
	}
	if err != nil {
		return event.Outcome{}, fmt.Errorf("failed to send prompt: %w", err)
	}
	last := lastAssistant(msgs)
	// A cancel can land after PiG finished the turn but before stop(); a
	// turn that ended normally is complete whatever the abort hook saw.
	if aborted.Load() && (last == nil || last.StopReason != ai.StopReasonStop) {
		return event.Outcome{Status: event.Interrupted}, nil
	}
	if last != nil {
		switch last.StopReason {
		case ai.StopReasonAborted:
			return event.Outcome{Status: event.Interrupted}, nil
		case ai.StopReasonError:
			return event.Outcome{Status: event.Failed, Message: last.ErrorMessage}, nil
		}
	}
	return event.Outcome{Status: event.Completed}, nil
}

// abortAtRunStart re-issues the abort at agent_start when ctx is already
// cancelled. PiG drops an abort requested before the run is marked busy
// (RequestAbort only records it while a run is active) and clears pending
// requests when the run starts; agent_start is emitted after that clear and
// before the first provider request, so aborting there stops the run as PiG's
// own agent_start check does.
func abortAtRunStart(ctx context.Context, abort func()) func(agent.AgentEvent) {
	return func(ev agent.AgentEvent) {
		if _, ok := ev.(agent.AgentStartEvent); ok && ctx.Err() != nil {
			abort()
		}
	}
}

func lastAssistant(msgs []agent.AgentMessage) *agent.AssistantMessage {
	for _, m := range slices.Backward(msgs) {
		if m.Assistant != nil {
			return m.Assistant
		}
	}
	return nil
}

// Close aborts any running turn, waits for it within ctx, and always releases
// the session.
func (s *Session) Close(ctx context.Context) error {
	abortErr := s.inner.Abort(ctx)
	closeErr := s.inner.Close()
	if abortErr != nil {
		return fmt.Errorf("failed to stop session run: %w: %w", ErrJoinTimeout, abortErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close session: %w", closeErr)
	}
	return nil
}
