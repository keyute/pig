package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/keyute/kopi/internal/event"
	"github.com/keyute/kopi/internal/pigruntime"
	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

type Config struct {
	Model     string // "provider/id"
	Workspace string
	Tools     []tool.Tool
	// JoinTimeout bounds how long a session switch or Close waits for running
	// work to stop.
	JoinTimeout time.Duration
}

// Controller holds all frontend-neutral behaviour: frontends pass input in
// and render the events they subscribe to.
//
// Lock order is life, then mu. mu is never held while calling into PiG or
// delivering events; life is held across both, which is safe because
// subscribers must not call back into the Controller.
type Controller struct {
	rt        *pigruntime.Runtime
	cfg       Config
	prompt    string
	authority *policy.Authority
	relay     relay
	wg        sync.WaitGroup

	// life serialises session start, switch and close.
	life sync.Mutex

	mu     sync.Mutex
	sess   *pigruntime.Session
	sessID string
	// emit is bound to sess's relay epoch.
	emit        func(event.Event)
	unsubscribe func()
	// switching refuses prompts while a session transition is under way, so
	// no work can start on a session that is about to be replaced.
	switching bool
	// closed is set by Close; afterwards the Controller refuses all input,
	// leaves the mode alone and delivers no events.
	closed  bool
	turn    *Work
	turnSeq int
}

func NewController(rt *pigruntime.Runtime, cfg Config) *Controller {
	return &Controller{
		rt:        rt,
		cfg:       cfg,
		prompt:    systemPrompt(cfg.Workspace, cfg.Tools),
		authority: policy.NewAuthority(),
	}
}

// Subscribe registers fn for every event of the current session plus
// controller events. fn runs synchronously; it must not block and must not
// call back into the Controller.
func (c *Controller) Subscribe(fn func(event.Event)) func() {
	return c.relay.subscribe(fn)
}

type OpenRequest struct {
	ResumeID string
	Continue bool
}

func (c *Controller) Open(ctx context.Context, req OpenRequest) error {
	return c.start(ctx, req)
}

func (c *Controller) NewSession(ctx context.Context) error {
	return c.start(ctx, OpenRequest{})
}

func (c *Controller) Resume(ctx context.Context, id string) error {
	return c.start(ctx, OpenRequest{ResumeID: id})
}

func (c *Controller) Sessions() ([]pigruntime.SessionInfo, error) {
	return c.rt.Sessions()
}

// Submit takes one line of human input. A control command is executed and
// returns (nil, nil), ErrQuit for /quit or ErrUnknownCommand. Anything else is
// a prompt that starts a root turn; the next prompt is accepted once the
// returned Work's Done is closed. After Close every input but a blank line
// returns ErrNoSession.
func (c *Controller) Submit(ctx context.Context, input string) (*Work, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, ErrNoSession
	}
	name, args, isCommand := parse(input)
	if !isCommand {
		prompt := strings.TrimSpace(input)
		if strings.HasPrefix(prompt, "//") {
			prompt = prompt[1:]
		}
		return c.startTurn(ctx, prompt)
	}
	cmds := commandTable()
	i := slices.IndexFunc(cmds, func(cmd command) bool { return cmd.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%w: /%s", ErrUnknownCommand, name)
	}
	return nil, cmds[i].run(c, ctx, args)
}

func (c *Controller) Commands() []Command {
	cmds := commandTable()
	out := make([]Command, 0, len(cmds))
	for _, cmd := range cmds {
		out = append(out, cmd.Command)
	}
	return out
}

// Interrupt requests abort of the running root turn without waiting for it.
func (c *Controller) Interrupt() bool {
	c.mu.Lock()
	w := c.turn
	c.mu.Unlock()
	if w == nil {
		return false
	}
	w.cancel()
	return true
}

func (c *Controller) EnterPlan() {
	c.changeMode(c.authority.EnterPlan)
}

func (c *Controller) EnterExecute() {
	c.changeMode(c.authority.EnterExecute)
}

// changeMode holds mu across the read and the change so that only the caller
// that actually changed the mode emits ModeChanged.
func (c *Controller) changeMode(enter func() uint64) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	_, before := c.authority.Snapshot()
	after := enter()
	mode, _ := c.authority.Snapshot()
	c.mu.Unlock()
	if after != before {
		c.relay.emit(event.ModeChanged{Mode: mode})
	}
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	sess, sessID := c.sess, c.sessID
	busy := c.turn != nil
	c.mu.Unlock()

	mode, _ := c.authority.Snapshot()
	s := Status{SessionID: sessID, Mode: mode, Model: c.cfg.Model, Workspace: c.cfg.Workspace, Busy: busy}
	if sess != nil {
		s.MessageCount = sess.MessageCount()
	}
	for _, t := range c.cfg.Tools {
		allowed := !t.Capability.NeedsExecute() || mode == policy.Execute
		s.Tools = append(s.Tools, ToolStatus{Name: t.Name, Capability: t.Capability, Allowed: allowed})
	}
	return s
}

// Close stops running work and closes the session within min(ctx,
// JoinTimeout). It does not close the Runtime. If work does not stop, the
// session is left open, the work stays tracked and the error wraps
// pigruntime.ErrJoinTimeout; a later Close tries again. No event is delivered
// after Close returns.
func (c *Controller) Close(ctx context.Context) error {
	c.life.Lock()
	defer c.life.Unlock()
	defer c.relay.shut()

	c.mu.Lock()
	c.closed = true
	sess, unsubscribe, w := c.sess, c.unsubscribe, c.turn
	c.mu.Unlock()
	if sess == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.JoinTimeout)
	defer cancel()
	if w != nil && !join(ctx, w) {
		return fmt.Errorf("failed to close controller: work %s did not stop: %w", w.id, pigruntime.ErrJoinTimeout)
	}
	c.wg.Wait()
	unsubscribe()
	c.mu.Lock()
	c.sess, c.sessID, c.emit, c.unsubscribe = nil, "", nil, nil
	c.mu.Unlock()
	if err := sess.Close(ctx); err != nil {
		return fmt.Errorf("failed to close controller: %w", err)
	}
	return nil
}

func (c *Controller) startTurn(ctx context.Context, prompt string) (*Work, error) {
	c.mu.Lock()
	if c.sess == nil || c.closed {
		c.mu.Unlock()
		return nil, ErrNoSession
	}
	if c.switching || c.turn != nil {
		c.mu.Unlock()
		return nil, ErrBusy
	}
	sess, emit := c.sess, c.emit
	// The turn outlives the Submit call: only Interrupt, a session switch or
	// Close may stop it, by cancelling this context. Cancelling it after the
	// turn has ended is harmless, so no abort can reach a later turn.
	turnCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.turnSeq++
	w := &Work{id: fmt.Sprintf("turn-%d", c.turnSeq), cancel: cancel, done: make(chan struct{})}
	c.turn = w
	c.mu.Unlock()

	c.wg.Go(func() { c.runTurn(turnCtx, sess, emit, w, prompt) })
	return w, nil
}

// runTurn emits TurnEnd before deregistering the work, so a transition that
// finds no work never races an undelivered TurnEnd of the old session.
func (c *Controller) runTurn(ctx context.Context, sess *pigruntime.Session, emit func(event.Event), w *Work, prompt string) {
	outcome, err := sess.Send(ctx, prompt)
	w.cancel()
	if err != nil {
		outcome = event.Outcome{Status: event.Failed, Message: err.Error()}
		emit(event.Error{Err: err})
	}
	w.outcome = outcome
	emit(event.TurnEnd{WorkID: w.id, Outcome: outcome})
	c.mu.Lock()
	if c.turn == w {
		c.turn = nil
	}
	c.mu.Unlock()
	close(w.done)
}

// start opens a session for req and makes it current. Running work is
// stopped and joined first; if it will not stop the switch is refused. The new
// session is opened before the old one is closed, so a failed open leaves the
// old session in place.
func (c *Controller) start(ctx context.Context, req OpenRequest) error {
	c.life.Lock()
	defer c.life.Unlock()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrNoSession
	}
	c.switching = true
	old, oldUnsubscribe, w := c.sess, c.unsubscribe, c.turn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.switching = false
		c.mu.Unlock()
	}()

	if w != nil {
		joinCtx, cancel := context.WithTimeout(ctx, c.cfg.JoinTimeout)
		joined := join(joinCtx, w)
		cancel()
		if !joined {
			return fmt.Errorf("failed to switch session: work %s did not stop: %w", w.id, ErrWorkStillRunning)
		}
	}

	sess, err := c.rt.Open(ctx, pigruntime.OpenOptions{
		Model:        c.cfg.Model,
		ResumeID:     req.ResumeID,
		Continue:     req.Continue,
		SystemPrompt: c.prompt,
		Tools:        c.cfg.Tools,
		Authority:    c.authority,
	})
	if err != nil {
		return err
	}

	c.relay.advance()
	if old != nil {
		oldUnsubscribe()
		// The old session is released even if the caller's ctx has ended:
		// the new session is already open.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.JoinTimeout)
		if err := old.Close(closeCtx); err != nil {
			c.relay.emit(event.Error{Err: err})
		}
		cancel()
	}
	// Execute mode is never carried into a session start.
	c.EnterPlan()

	emit := c.relay.bind()
	unsubscribe := sess.Subscribe(emit)
	id := sess.ID()
	c.mu.Lock()
	c.sess, c.sessID, c.emit, c.unsubscribe = sess, id, emit, unsubscribe
	c.mu.Unlock()
	c.relay.emit(event.SessionChanged{
		SessionID:    id,
		Resumed:      req.ResumeID != "" || req.Continue,
		MessageCount: sess.MessageCount(),
	})
	return nil
}

// join cancels w and reports whether it finished before ctx ended.
func join(ctx context.Context, w *Work) bool {
	w.cancel()
	select {
	case <-w.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Controller) notice(text string) {
	c.relay.emit(event.Notice{Text: text})
}
