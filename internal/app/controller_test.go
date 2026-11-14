package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/keyute/kopi/internal/event"
	"github.com/keyute/kopi/internal/pigruntime"
	"github.com/keyute/kopi/internal/pigruntime/pigruntimetest"
	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

const (
	fauxProvider = "kopi-faux"
	execOutput   = "exec-output-91c2"
	// waitLimit only guards against a hung test; no test relies on it elapsing.
	waitLimit = 30 * time.Second
)

type recorder struct {
	mu  sync.Mutex
	evs []event.Event
}

func (r *recorder) add(ev event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func (r *recorder) all() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.evs)
}

func ofType[T event.Event](evs []event.Event) []T {
	var out []T
	for _, ev := range evs {
		if e, ok := ev.(T); ok {
			out = append(out, e)
		}
	}
	return out
}

func lastNotice(t *testing.T, rec *recorder) string {
	t.Helper()
	notices := ofType[event.Notice](rec.all())
	require.NotEmpty(t, notices)
	return notices[len(notices)-1].Text
}

type harness struct {
	c    *Controller
	faux *ai.FauxProviderHandle
	rec  *recorder
}

// newHarness builds a controller over a fresh runtime on sb. Its cleanup
// closes the controller before the runtime.
func newHarness(t *testing.T, sb pigruntimetest.Sandbox, cfg Config) *harness {
	t.Helper()
	rt := pigruntimetest.NewRuntime(t, sb)
	faux, model := pigruntimetest.RegisterFaux(t, rt, fauxProvider)
	if cfg.Model == "" {
		cfg.Model = model
	}
	cfg.Workspace = sb.Workspace
	if cfg.JoinTimeout == 0 {
		cfg.JoinTimeout = waitLimit
	}
	c := NewController(rt, cfg)
	rec := &recorder{}
	c.Subscribe(rec.add)
	t.Cleanup(func() { assert.NoError(t, c.Close(context.Background())) })
	return &harness{c: c, faux: faux, rec: rec}
}

func openHarness(t *testing.T, cfg Config) (*harness, pigruntimetest.Sandbox) {
	t.Helper()
	sb := pigruntimetest.NewSandbox(t)
	h := newHarness(t, sb, cfg)
	require.NoError(t, h.c.Open(t.Context(), OpenRequest{}))
	return h, sb
}

func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting")
	}
}

func (h *harness) turn(t *testing.T, prompt string) *Work {
	t.Helper()
	w, err := h.c.Submit(t.Context(), prompt)
	require.NoError(t, err)
	require.NotNil(t, w)
	wait(t, w.Done())
	return w
}

func testTool(name string, c policy.Capability, run func(context.Context, json.RawMessage) (string, error)) tool.Tool {
	return tool.Tool{
		Name:        name,
		Label:       name,
		Description: "Test-only tool.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Capability:  c,
		Run:         run,
	}
}

func execTool(ran *atomic.Int32) tool.Tool {
	return testTool("Exec", policy.Exec, func(context.Context, json.RawMessage) (string, error) {
		ran.Add(1)
		return execOutput, nil
	})
}

// releaseTool blocks until release is closed, ignoring its ctx.
func releaseTool(started chan<- struct{}, release <-chan struct{}) tool.Tool {
	return testTool("Hold", policy.Read, func(context.Context, json.RawMessage) (string, error) {
		close(started)
		<-release
		return "released", nil
	})
}

func readTool(t *testing.T, sb pigruntimetest.Sandbox) tool.Tool {
	t.Helper()
	ws, err := tool.NewWorkspace(sb.Workspace, nil)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ws.Close()) })
	return tool.NewRead(ws)
}

func call(name, id string) pigruntimetest.Call {
	return pigruntimetest.Call{Name: name, ID: id}
}

func receive(t *testing.T, requests <-chan pigruntimetest.Request) pigruntimetest.Request {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for a provider request")
		return pigruntimetest.Request{}
	}
}

func TestTurnStreamsTextThenEnds(t *testing.T) {
	h, _ := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("hello from the faux model")})

	w := h.turn(t, "hi")

	evs := h.rec.all()
	var text strings.Builder
	for _, d := range ofType[event.TextDelta](evs) {
		text.WriteString(d.Text)
	}
	assert.Equal(t, "hello from the faux model", text.String())
	assert.Greater(t, len(ofType[event.TextDelta](evs)), 1, "the reply streams in several deltas")
	assert.Equal(t, event.TurnEnd{WorkID: w.ID(), Outcome: event.Outcome{Status: event.Completed}}, evs[len(evs)-1])
	assert.Equal(t, event.Outcome{Status: event.Completed}, w.Outcome())
}

func TestReadResultReachesNextRequest(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(sb.Workspace, "notes.txt"), []byte("kopi fixture 42\n"), 0o600))
	h := newHarness(t, sb, Config{Tools: []tool.Tool{readTool(t, sb)}})
	require.NoError(t, h.c.Open(t.Context(), OpenRequest{}))
	requests := make(chan pigruntimetest.Request, 1)
	h.faux.SetResponses([]ai.FauxResponseStep{
		pigruntimetest.ToolCalls(pigruntimetest.Call{Name: "Read", ID: "call-1", Args: map[string]any{"file_path": "notes.txt"}}),
		pigruntimetest.Capture(t, requests, "read it"),
	})

	h.turn(t, "read notes.txt")

	evs := h.rec.all()
	assert.Equal(t, []event.ToolStart{{CallID: "call-1", Name: "Read", Args: `{"file_path":"notes.txt"}`}}, ofType[event.ToolStart](evs))
	ends := ofType[event.ToolEnd](evs)
	require.Len(t, ends, 1)
	assert.False(t, ends[0].IsError)
	assert.Contains(t, ends[0].Output, "kopi fixture 42")
	req := receive(t, requests)
	require.Len(t, req.ToolResults, 1)
	assert.Contains(t, req.ToolResults[0], "kopi fixture 42")
}

func TestExecToolFollowsMode(t *testing.T) {
	var ran atomic.Int32
	h, _ := openHarness(t, Config{Tools: []tool.Tool{execTool(&ran)}})
	requests := make(chan pigruntimetest.Request, 1)
	turn := func() string {
		h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Exec", "call-1")), pigruntimetest.Capture(t, requests, "ok")})
		h.turn(t, "run it")
		req := receive(t, requests)
		require.NotEmpty(t, req.ToolResults)
		return req.ToolResults[len(req.ToolResults)-1]
	}

	denied := turn()
	assert.Contains(t, denied, `tool "Exec" needs execute mode; the session is in plan mode`)
	assert.Zero(t, ran.Load())

	h.c.EnterExecute()
	assert.Equal(t, execOutput, turn())
	assert.Equal(t, int32(1), ran.Load())

	h.c.EnterPlan()
	assert.Contains(t, turn(), policy.ErrDenied.Error())
	assert.Equal(t, int32(1), ran.Load())

	assert.Equal(t, []event.ModeChanged{{Mode: policy.Execute}, {Mode: policy.Plan}}, ofType[event.ModeChanged](h.rec.all()))
}

// PiG runs a batch containing a sequential (non-Read) tool one call at a
// time, so the Exec call's before-hook runs only after the blocking call ends
// and the call is denied there; Admit's revocation is covered in pigruntime.
func TestPlanCommandStopsLaterExecCallInSameTurn(t *testing.T) {
	var ran atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{releaseTool(started, release), execTool(&ran)}})
	requests := make(chan pigruntimetest.Request, 1)
	h.faux.SetResponses([]ai.FauxResponseStep{
		pigruntimetest.ToolCalls(call("Hold", "call-1"), call("Exec", "call-2")),
		pigruntimetest.Capture(t, requests, "done"),
	})
	h.c.EnterExecute()

	w, err := h.c.Submit(t.Context(), "hold then exec")
	require.NoError(t, err)
	wait(t, started)
	_, err = h.c.Submit(t.Context(), "/plan")
	require.NoError(t, err)
	close(release)
	wait(t, w.Done())

	assert.Zero(t, ran.Load())
	req := receive(t, requests)
	require.Len(t, req.ToolResults, 2)
	assert.Equal(t, "released", req.ToolResults[0])
	assert.Contains(t, req.ToolResults[1], `tool "Exec" needs execute mode; the session is in plan mode`)
	assert.Equal(t, policy.Plan, h.c.Status().Mode)
}

func TestProviderFailureEndsTurnFailed(t *testing.T) {
	h, _ := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ProviderError("invalid request: bad thing")})

	w := h.turn(t, "go")

	want := event.Outcome{Status: event.Failed, Message: "invalid request: bad thing"}
	assert.Equal(t, want, w.Outcome())
	assert.Equal(t, []event.TurnEnd{{WorkID: w.ID(), Outcome: want}}, ofType[event.TurnEnd](h.rec.all()))
}

func TestInterruptDuringToolEndsInterruptedAndPersists(t *testing.T) {
	started := make(chan struct{})
	block := testTool("Block", policy.Read, func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	h, sb := openHarness(t, Config{Tools: []tool.Tool{block}})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Block", "call-1")), pigruntimetest.Text("unreached")})
	assert.False(t, h.c.Interrupt(), "nothing to interrupt yet")

	w, err := h.c.Submit(t.Context(), "block")
	require.NoError(t, err)
	wait(t, started)
	assert.True(t, h.c.Interrupt())
	wait(t, w.Done())

	assert.Equal(t, event.Outcome{Status: event.Interrupted}, w.Outcome())
	assert.Equal(t, []event.TurnEnd{{WorkID: w.ID(), Outcome: event.Outcome{Status: event.Interrupted}}}, ofType[event.TurnEnd](h.rec.all()))
	id := h.c.Status().SessionID
	require.NoError(t, h.c.Close(t.Context()))

	fresh := newHarness(t, sb, Config{Tools: []tool.Tool{block}})
	require.NoError(t, fresh.c.Open(t.Context(), OpenRequest{ResumeID: id}))
	changed := ofType[event.SessionChanged](fresh.rec.all())
	require.Len(t, changed, 1)
	assert.Equal(t, id, changed[0].SessionID)
	assert.True(t, changed[0].Resumed)
	// user, tool call, tool result and PiG's synthesized error message
	assert.Equal(t, 4, changed[0].MessageCount)

	requests := make(chan pigruntimetest.Request, 1)
	fresh.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Capture(t, requests, "resumed")})
	fresh.turn(t, "carry on")
	req := receive(t, requests)
	assert.Equal(t, []string{"block", "carry on"}, req.UserTexts)
	assert.Equal(t, []string{context.Canceled.Error()}, req.ToolResults)
	assert.Contains(t, req.Raw, `"call-1"`)
}

// blockOnFirstTurnEnd holds event delivery at the first TurnEnd until unblock
// is closed, keeping the finished turn registered in that window.
func blockOnFirstTurnEnd(c *Controller) (reached, unblock chan struct{}) {
	reached, unblock = make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.Subscribe(func(ev event.Event) {
		if _, ok := ev.(event.TurnEnd); ok {
			once.Do(func() {
				close(reached)
				<-unblock
			})
		}
	})
	return reached, unblock
}

func TestInterruptAfterTurnFinishedSparesNextTurn(t *testing.T) {
	h, _ := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("first"), pigruntimetest.Text("second")})
	reached, unblock := blockOnFirstTurnEnd(h.c)

	w, err := h.c.Submit(t.Context(), "first")
	require.NoError(t, err)
	wait(t, reached)
	assert.True(t, h.c.Interrupt(), "the finished turn is still registered")
	close(unblock)
	wait(t, w.Done())
	assert.Equal(t, event.Completed, w.Outcome().Status)

	assert.Equal(t, event.Outcome{Status: event.Completed}, h.turn(t, "second").Outcome())
	assert.Equal(t, 2, h.faux.CallCount())
}

func TestFailedResumeAsTurnFinishesKeepsSessionUsable(t *testing.T) {
	h, _ := openHarness(t, Config{JoinTimeout: 50 * time.Millisecond})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("first"), pigruntimetest.Text("second")})
	reached, unblock := blockOnFirstTurnEnd(h.c)
	id := h.c.Status().SessionID

	w, err := h.c.Submit(t.Context(), "first")
	require.NoError(t, err)
	wait(t, reached)
	_, err = h.c.Submit(t.Context(), "/resume no-such-session")
	assert.ErrorIs(t, err, ErrWorkStillRunning)
	close(unblock)
	wait(t, w.Done())

	_, err = h.c.Submit(t.Context(), "/resume no-such-session")
	require.ErrorIs(t, err, pigruntime.ErrSessionNotFound)
	assert.Equal(t, id, h.c.Status().SessionID)
	assert.Equal(t, event.Outcome{Status: event.Completed}, h.turn(t, "second").Outcome())
	assert.Equal(t, 2, h.faux.CallCount())
}

func TestPromptWhileBusyIsRefusedButStatusWorks(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{releaseTool(started, release)}})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Hold", "call-1")), pigruntimetest.Text("done")})

	w, err := h.c.Submit(t.Context(), "hold")
	require.NoError(t, err)
	wait(t, started)

	_, err = h.c.Submit(t.Context(), "another prompt")
	require.ErrorIs(t, err, ErrBusy)
	got, err := h.c.Submit(t.Context(), "/status")
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Contains(t, lastNotice(t, h.rec), "turn: running")
	assert.True(t, h.c.Status().Busy)

	close(release)
	wait(t, w.Done())
	assert.False(t, h.c.Status().Busy)
}

func TestUnknownCommandNeverReachesProvider(t *testing.T) {
	h, _ := openHarness(t, Config{})
	requests := make(chan pigruntimetest.Request, 1)
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Capture(t, requests, "ok")})

	w, err := h.c.Submit(t.Context(), "/frobnicate now")
	require.ErrorIs(t, err, ErrUnknownCommand)
	assert.Contains(t, err.Error(), "/frobnicate")
	assert.Nil(t, w)
	w, err = h.c.Submit(t.Context(), "   ")
	require.NoError(t, err)
	assert.Nil(t, w)
	assert.Zero(t, h.faux.CallCount())

	h.turn(t, "//hello")
	req := receive(t, requests)
	assert.Equal(t, []string{"/hello"}, req.UserTexts)
}

func TestModelOutputCannotChangeMode(t *testing.T) {
	echo := testTool("Echo", policy.Read, func(context.Context, json.RawMessage) (string, error) {
		return "/execute", nil
	})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{echo}})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Echo", "call-1")), pigruntimetest.Text("/execute")})

	h.turn(t, "try to escalate")

	evs := h.rec.all()
	assert.Equal(t, "/execute", ofType[event.ToolEnd](evs)[0].Output)
	assert.Equal(t, policy.Plan, h.c.Status().Mode)
	assert.Empty(t, ofType[event.ModeChanged](evs))
}

func TestNewSessionIsRefusedWhileWorkWillNotStop(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{releaseTool(started, release)}, JoinTimeout: 50 * time.Millisecond})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Hold", "call-1")), pigruntimetest.Text("done")})
	oldID := h.c.Status().SessionID

	w, err := h.c.Submit(t.Context(), "hold")
	require.NoError(t, err)
	wait(t, started)

	_, err = h.c.Submit(t.Context(), "/new")
	require.ErrorIs(t, err, ErrWorkStillRunning)
	status := h.c.Status()
	assert.Equal(t, oldID, status.SessionID)
	assert.True(t, status.Busy)

	close(release)
	wait(t, w.Done())
	_, err = h.c.Submit(t.Context(), "/new")
	require.NoError(t, err)

	evs := h.rec.all()
	changedAt := slices.IndexFunc(evs, func(ev event.Event) bool {
		changed, ok := ev.(event.SessionChanged)
		return ok && changed.SessionID != oldID
	})
	require.Positive(t, changedAt)
	assert.Equal(t, event.SessionChanged{SessionID: h.c.Status().SessionID}, evs[changedAt])
	assert.NotEqual(t, oldID, h.c.Status().SessionID)
	assert.Len(t, evs, changedAt+1, "no event may follow the switch")
	assert.Equal(t, []event.TurnEnd{{WorkID: w.ID(), Outcome: event.Outcome{Status: event.Interrupted}}}, ofType[event.TurnEnd](evs))
	assert.Empty(t, ofType[event.Error](evs))
}

func TestNewSessionStopsRunningTurn(t *testing.T) {
	started := make(chan struct{})
	block := testTool("Block", policy.Read, func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{block}})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Block", "call-1")), pigruntimetest.Text("unreached")})
	oldID := h.c.Status().SessionID

	w, err := h.c.Submit(t.Context(), "block")
	require.NoError(t, err)
	wait(t, started)
	_, err = h.c.Submit(t.Context(), "/new")
	require.NoError(t, err)

	select {
	case <-w.Done():
	default:
		t.Fatal("/new returned before the turn ended")
	}
	assert.Equal(t, event.Outcome{Status: event.Interrupted}, w.Outcome())
	newID := h.c.Status().SessionID
	assert.NotEqual(t, oldID, newID)
	evs := h.rec.all()
	endAt := slices.Index(evs, event.Event(event.TurnEnd{WorkID: w.ID(), Outcome: event.Outcome{Status: event.Interrupted}}))
	changedAt := slices.Index(evs, event.Event(event.SessionChanged{SessionID: newID}))
	require.GreaterOrEqual(t, endAt, 0)
	assert.Less(t, endAt, changedAt)

	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("fresh")})
	assert.Equal(t, event.Outcome{Status: event.Completed}, h.turn(t, "after switch").Outcome())
}

func TestPromptDuringSessionSwitchIsRefused(t *testing.T) {
	h, _ := openHarness(t, Config{})
	oldID := h.c.Status().SessionID
	reached, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.c.Subscribe(func(ev event.Event) {
		if changed, ok := ev.(event.SessionChanged); ok && changed.SessionID != oldID {
			once.Do(func() {
				close(reached)
				<-unblock
			})
		}
	})

	switched := make(chan error, 1)
	go func() {
		_, err := h.c.Submit(context.Background(), "/new")
		switched <- err
	}()
	wait(t, reached)
	w, err := h.c.Submit(t.Context(), "too early")
	close(unblock)
	assert.ErrorIs(t, err, ErrBusy)
	assert.Nil(t, w)

	select {
	case err := <-switched:
		require.NoError(t, err)
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for /new")
	}
	assert.Zero(t, h.faux.CallCount())
}

func TestSessionStartAlwaysEntersPlan(t *testing.T) {
	h, sb := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("first answer")})
	h.turn(t, "first question")
	savedID := h.c.Status().SessionID

	h.c.EnterExecute()
	require.NoError(t, h.c.NewSession(t.Context()))
	assert.Equal(t, policy.Plan, h.c.Status().Mode)
	assert.Equal(t, []event.ModeChanged{{Mode: policy.Execute}, {Mode: policy.Plan}}, ofType[event.ModeChanged](h.rec.all()))

	h.c.EnterExecute()
	require.NoError(t, h.c.Close(t.Context()))

	fresh := newHarness(t, sb, Config{})
	require.NoError(t, fresh.c.Open(t.Context(), OpenRequest{ResumeID: savedID}))
	changed := ofType[event.SessionChanged](fresh.rec.all())
	require.Len(t, changed, 1)
	assert.Equal(t, savedID, changed[0].SessionID)
	assert.True(t, changed[0].Resumed)
	assert.Positive(t, changed[0].MessageCount)
	assert.Equal(t, policy.Plan, fresh.c.Status().Mode)
}

func TestResumeEntersPlan(t *testing.T) {
	h, _ := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("first answer")})
	h.turn(t, "first question")
	savedID := h.c.Status().SessionID
	require.NoError(t, h.c.NewSession(t.Context()))

	h.c.EnterExecute()
	_, err := h.c.Submit(t.Context(), "/resume "+savedID)
	require.NoError(t, err)

	status := h.c.Status()
	assert.Equal(t, savedID, status.SessionID)
	assert.Equal(t, policy.Plan, status.Mode)
	assert.Positive(t, status.MessageCount)
}

func TestResumeWithoutIDListsSessions(t *testing.T) {
	h, _ := openHarness(t, Config{})
	_, err := h.c.Submit(t.Context(), "/resume")
	require.NoError(t, err)
	assert.Equal(t, "no saved sessions for this workspace", lastNotice(t, h.rec))

	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("answer")})
	h.turn(t, "list me")
	_, err = h.c.Submit(t.Context(), "/resume")
	require.NoError(t, err)
	notice := lastNotice(t, h.rec)
	assert.Contains(t, notice, h.c.Status().SessionID)
	assert.Contains(t, notice, "list me")
	assert.NotContains(t, notice, "messages")
}

func TestOpenErrorsPassThrough(t *testing.T) {
	codex := ai.GetBuiltinModels("openai-codex")
	require.NotEmpty(t, codex)
	for _, tc := range []struct {
		name  string
		model string
		req   OpenRequest
		want  error
	}{
		{name: "continue without sessions", req: OpenRequest{Continue: true}, want: pigruntime.ErrSessionNotFound},
		{name: "unknown model", model: fauxProvider + "/missing", want: pigruntime.ErrUnknownModel},
		{name: "missing credential", model: "openai-codex/" + codex[0].ID, want: pigruntime.ErrNoCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, pigruntimetest.NewSandbox(t), Config{Model: tc.model})
			require.ErrorIs(t, h.c.Open(t.Context(), tc.req), tc.want)
			_, err := h.c.Submit(t.Context(), "hello")
			assert.ErrorIs(t, err, ErrNoSession)
		})
	}
}

func TestStatusReportsToolsHonestly(t *testing.T) {
	var ran atomic.Int32
	sb := pigruntimetest.NewSandbox(t)
	h := newHarness(t, sb, Config{Tools: []tool.Tool{readTool(t, sb), execTool(&ran)}})
	require.NoError(t, h.c.Open(t.Context(), OpenRequest{}))

	status := h.c.Status()
	assert.Equal(t, []ToolStatus{{"Read", policy.Read, true}, {"Exec", policy.Exec, false}}, status.Tools)
	assert.Equal(t, sb.Workspace, status.Workspace)
	assert.Equal(t, fauxProvider+"/faux-1", status.Model)

	_, err := h.c.Submit(t.Context(), "/status")
	require.NoError(t, err)
	notice := lastNotice(t, h.rec)
	for _, want := range []string{
		"mode: plan", "model: " + status.Model, "workspace: " + sb.Workspace, "session: " + status.SessionID + " (0 messages)",
		"turn: idle", "Read (read): allowed", "Exec (exec): not allowed in plan mode", noMutatingTools,
	} {
		assert.Contains(t, notice, want)
	}

	h.c.EnterExecute()
	assert.Equal(t, []ToolStatus{{"Read", policy.Read, true}, {"Exec", policy.Exec, true}}, h.c.Status().Tools)
}

func TestHelpListsCommands(t *testing.T) {
	h, _ := openHarness(t, Config{})
	_, err := h.c.Submit(t.Context(), "/help")
	require.NoError(t, err)
	notice := lastNotice(t, h.rec)
	for _, cmd := range h.c.Commands() {
		assert.Contains(t, notice, cmd.Usage)
	}
	_, err = h.c.Submit(t.Context(), "/quit")
	assert.ErrorIs(t, err, ErrQuit)
}

func TestTurnUsesNoAmbientState(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	sb.PlantDecoys(t)
	require.NoError(t, os.WriteFile(filepath.Join(sb.Workspace, "notes.txt"), []byte("kopi fixture 42\n"), 0o600))
	before := pigruntimetest.Snapshot(t, sb.Home, sb.Workspace)
	tools := []tool.Tool{readTool(t, sb)}
	h := newHarness(t, sb, Config{Tools: tools})
	require.NoError(t, h.c.Open(t.Context(), OpenRequest{}))
	requests := make(chan pigruntimetest.Request, 1)
	h.faux.SetResponses([]ai.FauxResponseStep{
		pigruntimetest.ToolCalls(pigruntimetest.Call{Name: "Read", ID: "call-1", Args: map[string]any{"file_path": "notes.txt"}}),
		pigruntimetest.Capture(t, requests, "done"),
	})

	h.turn(t, "read notes.txt")
	require.NoError(t, h.c.Close(t.Context()))

	req := receive(t, requests)
	assert.Equal(t, []string{"Read"}, req.ToolNames)
	assert.Equal(t, systemPrompt(sb.Workspace, tools), req.SystemPrompt)
	for _, marker := range pigruntimetest.DecoyMarkers {
		assert.NotContains(t, req.Raw, marker)
	}
	assert.Equal(t, before, pigruntimetest.Snapshot(t, sb.Home, sb.Workspace))
}

func TestCloseJoinsRunningTurn(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	started := make(chan struct{})
	block := testTool("Block", policy.Read, func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	h := newHarness(t, sb, Config{Tools: []tool.Tool{block}})
	ignore := goleak.IgnoreCurrent()
	require.NoError(t, h.c.Open(t.Context(), OpenRequest{}))
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Block", "call-1")), pigruntimetest.Text("unreached")})

	w, err := h.c.Submit(t.Context(), "block")
	require.NoError(t, err)
	wait(t, started)
	require.NoError(t, h.c.Close(t.Context()))

	select {
	case <-w.Done():
	default:
		t.Fatal("Close returned before the turn ended")
	}
	assert.Equal(t, event.Interrupted, w.Outcome().Status)
	goleak.VerifyNone(t, ignore)
}

func TestCloseKeepsSessionWhileWorkWillNotStop(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := openHarness(t, Config{Tools: []tool.Tool{releaseTool(started, release)}, JoinTimeout: 50 * time.Millisecond})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.ToolCalls(call("Hold", "call-1")), pigruntimetest.Text("done")})
	id := h.c.Status().SessionID

	w, err := h.c.Submit(t.Context(), "hold")
	require.NoError(t, err)
	wait(t, started)

	// Assertions only until the tool is released, so a failure cannot hang the test.
	assert.ErrorIs(t, h.c.Close(t.Context()), pigruntime.ErrJoinTimeout)
	assert.ErrorIs(t, h.c.Close(t.Context()), pigruntime.ErrJoinTimeout)
	status := h.c.Status()
	assert.Equal(t, id, status.SessionID)
	assert.True(t, status.Busy)
	_, err = h.c.Submit(t.Context(), "after close")
	assert.ErrorIs(t, err, ErrNoSession)
	assertInertAfterClose(t, h)
	delivered := len(h.rec.all())

	close(release)
	wait(t, w.Done())
	assert.Len(t, h.rec.all(), delivered, "no event may be delivered after Close returned")
	require.NoError(t, h.c.Close(t.Context()))
	assert.Empty(t, h.c.Status().SessionID)
}

// assertInertAfterClose checks that a closed controller refuses commands,
// keeps its mode and delivers nothing.
func assertInertAfterClose(t *testing.T, h *harness) {
	t.Helper()
	delivered := len(h.rec.all())
	mode, gen := h.c.authority.Snapshot()
	for _, cmd := range []string{"/execute", "/status", "/help", "/plan"} {
		_, err := h.c.Submit(t.Context(), cmd)
		assert.ErrorIs(t, err, ErrNoSession, cmd)
	}
	h.c.EnterExecute()
	h.c.EnterPlan()
	gotMode, gotGen := h.c.authority.Snapshot()
	assert.Equal(t, mode, gotMode)
	assert.Equal(t, gen, gotGen)
	assert.Len(t, h.rec.all(), delivered, "no event may be delivered after Close returned")
}

func TestClosedControllerIsInert(t *testing.T) {
	h, _ := openHarness(t, Config{})
	h.faux.SetResponses([]ai.FauxResponseStep{pigruntimetest.Text("answer")})
	h.turn(t, "hello")
	require.NoError(t, h.c.Close(t.Context()))
	assertInertAfterClose(t, h)
}
