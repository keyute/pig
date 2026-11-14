package line

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/app"
	"github.com/keyute/kopi/internal/event"
	"github.com/keyute/kopi/internal/pigruntime/pigruntimetest"
	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

// waitLimit only guards against a hung test; no test relies on it elapsing.
const waitLimit = 30 * time.Second

// output is a writer whose content tests can wait on.
type output struct {
	mu      sync.Mutex
	b       strings.Builder
	changed chan struct{}
}

func newOutput() *output { return &output{changed: make(chan struct{})} }

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.b.Write(p)
	close(o.changed)
	o.changed = make(chan struct{})
	return len(p), nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func (o *output) waitFor(t *testing.T, s string) {
	t.Helper()
	for {
		o.mu.Lock()
		found, ch := strings.Contains(o.b.String(), s), o.changed
		o.mu.Unlock()
		if found {
			return
		}
		select {
		case <-ch:
		case <-time.After(waitLimit):
			t.Fatalf("timed out waiting for %q in output:\n%s", s, o.String())
		}
	}
}

type harness struct {
	c          *app.Controller
	faux       *ai.FauxProviderHandle
	out        *output
	interrupts chan os.Signal
	result     chan error
}

// start opens a controller over a faux provider and runs a frontend on in.
// The frontend subscribes before Open, as the root command does.
func start(t *testing.T, sb pigruntimetest.Sandbox, in io.Reader, interactive bool, steps []ai.FauxResponseStep, tools ...tool.Tool) *harness {
	t.Helper()
	rt := pigruntimetest.NewRuntime(t, sb)
	faux, model := pigruntimetest.RegisterFaux(t, rt, "kopi-faux")
	faux.SetResponses(steps)
	c := app.NewController(rt, app.Config{Model: model, Workspace: sb.Workspace, Tools: tools, JoinTimeout: waitLimit})
	t.Cleanup(func() { assert.NoError(t, c.Close(context.Background())) })
	h := &harness{c: c, faux: faux, out: newOutput(), interrupts: make(chan os.Signal), result: make(chan error, 1)}
	f := New(c, in, h.out, interactive)
	require.NoError(t, c.Open(t.Context(), app.OpenRequest{}))
	go func() { h.result <- f.Run(t.Context(), h.interrupts) }()
	return h
}

func (h *harness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.result:
		return err
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for Run to return; output:\n%s", h.out.String())
		return nil
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting")
	}
}

// pipe returns the reading end for the frontend and closes the writing end at
// cleanup, so the reader goroutine always ends.
func pipe(t *testing.T) (*io.PipeReader, *io.PipeWriter) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	return pr, pw
}

func send(t *testing.T, pw *io.PipeWriter, line string) {
	t.Helper()
	_, err := io.WriteString(pw, line+"\n")
	require.NoError(t, err)
}

func testTool(name string, run func(context.Context, json.RawMessage) (string, error)) tool.Tool {
	return tool.Tool{
		Name: name, Label: name, Description: "Test-only tool.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Capability: policy.Read,
		Run:        run,
	}
}

// holdTool blocks until release is closed, ignoring its ctx.
func holdTool(started chan<- struct{}, release <-chan struct{}) tool.Tool {
	return testTool("Hold", func(context.Context, json.RawMessage) (string, error) {
		close(started)
		<-release
		return "released", nil
	})
}

func toolCall(name string, args map[string]any) ai.FauxResponseStep {
	return pigruntimetest.ToolCalls(pigruntimetest.Call{Name: name, ID: "call-1", Args: args})
}

func ordered(t *testing.T, s string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(s[at:], p)
		require.GreaterOrEqual(t, i, 0, "%q not found in order in output:\n%s", p, s)
		at += i + len(p)
	}
}

func TestScriptedSessionRendersTurnThenStatus(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(sb.Workspace, "notes.txt"), []byte("fixture-body-77\n"), 0o600))
	ws, err := tool.NewWorkspace(sb.Workspace, nil)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ws.Close()) })
	steps := []ai.FauxResponseStep{
		ai.FauxAssistantMessage(ai.FauxContentBlocks{
			ai.FauxText("Let me read the notes."),
			ai.FauxToolCall("Read", map[string]any{"file_path": "notes.txt"}, &ai.FauxToolCallOptions{ID: "call-1"}),
		}, ai.FauxAssistantMessageOptions{StopReason: string(ai.StopReasonToolUse)}),
		pigruntimetest.Text("The notes are short."),
	}

	h := start(t, sb, strings.NewReader("read the notes\n/status\n/quit\n"), false, steps, tool.NewRead(ws))
	require.NoError(t, h.wait(t))

	out := h.out.String()
	ordered(t, out,
		"[session] ",
		"Let me read the notes.\n",
		`[tool] Read {"file_path":"notes.txt"}`,
		"[tool] Read ok\n",
		"The notes are short.\n",
		"mode: plan",
		"turn: idle",
	)
	assert.NotContains(t, out, "fixture-body-77", "tool output bodies are never printed")
	assert.NotContains(t, out, "plan> ", "no prompt marker without a terminal")
	for l := range strings.Lines(out) {
		if i := strings.Index(l, "[tool]"); i >= 0 {
			assert.Zero(t, i, "status line glued to other output: %q", l)
		}
	}
}

// eofReader closes eof once a Read has returned io.EOF.
type eofReader struct {
	r    io.Reader
	once sync.Once
	eof  chan struct{}
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.once.Do(func() { close(e.eof) })
	}
	return n, err
}

// The tool is released only after the frontend's reader has hit EOF, and Run
// unsubscribes when it returns, so the turn's final text and the end of its
// line can only be in the output if Run was still running when the turn
// ended. What this cannot prove without a hook in the frontend is that its
// loop had already taken the EOF from the reader before the turn ended: a
// Run that wrongly returned at EOF would be caught whenever the loop sees EOF
// before the remaining provider round trip completes.
func TestEOFWaitsForRunningTurn(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "piped", true: "interactive"}[interactive], func(t *testing.T) {
			sb := pigruntimetest.NewSandbox(t)
			started, release := make(chan struct{}), make(chan struct{})
			pr, pw := pipe(t)
			in := &eofReader{r: pr, eof: make(chan struct{})}
			h := start(t, sb, in, interactive, []ai.FauxResponseStep{toolCall("Hold", nil), pigruntimetest.Text("done holding")}, holdTool(started, release))

			send(t, pw, "hold")
			waitClosed(t, started)
			require.NoError(t, pw.Close())
			waitClosed(t, in.eof)
			close(release)

			require.NoError(t, h.wait(t))
			out := h.out.String()
			ordered(t, out, "[tool] Hold ok\n", "done holding\n")
			assert.True(t, strings.HasSuffix(out, "done holding\n") || strings.HasSuffix(out, "done holding\nplan> "), "the turn ended before Run returned:\n%s", out)
		})
	}
}

func TestUnknownCommandPrintsErrorAndContinues(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	h := start(t, sb, strings.NewReader("/nope\n/status\n/quit\n"), false, nil)

	require.NoError(t, h.wait(t))
	ordered(t, h.out.String(), "[error] unknown command: /nope\n", "mode: plan")
}

func TestInterruptDuringToolEndsTurn(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	started := make(chan struct{})
	block := testTool("Block", func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	pr, pw := pipe(t)
	h := start(t, sb, pr, false, []ai.FauxResponseStep{toolCall("Block", nil), pigruntimetest.Text("unreached")}, block)

	send(t, pw, "block")
	waitClosed(t, started)
	h.interrupts <- os.Interrupt
	h.out.waitFor(t, "[interrupted]\n")
	require.NoError(t, pw.Close())

	require.NoError(t, h.wait(t))
	assert.NotContains(t, h.out.String(), "unreached")
}

func TestInterruptAtIdleExits(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	pr, _ := pipe(t)
	h := start(t, sb, pr, true, nil)

	h.out.waitFor(t, "plan> ")
	h.interrupts <- os.Interrupt

	require.NoError(t, h.wait(t))
}

func TestInteractiveCommandsWorkMidTurn(t *testing.T) {
	sb := pigruntimetest.NewSandbox(t)
	started, release := make(chan struct{}), make(chan struct{})
	pr, pw := pipe(t)
	h := start(t, sb, pr, true, []ai.FauxResponseStep{toolCall("Hold", nil), pigruntimetest.Text("done holding")}, holdTool(started, release))

	h.out.waitFor(t, "plan> ")
	send(t, pw, "hold")
	waitClosed(t, started)
	send(t, pw, "/execute")
	h.out.waitFor(t, "[mode] execute\n")
	send(t, pw, "another prompt")
	h.out.waitFor(t, "[busy] a turn is running")
	send(t, pw, "/plan")
	h.out.waitFor(t, "[mode] plan\n")
	send(t, pw, "/status")
	h.out.waitFor(t, "turn: running")
	close(release)
	h.out.waitFor(t, "done holding\nplan> ")
	send(t, pw, "/execute")
	h.out.waitFor(t, "execute> ")
	send(t, pw, "/quit")

	require.NoError(t, h.wait(t))
	assert.Equal(t, 2, h.faux.CallCount(), "only the held turn's two requests; the busy prompt never reached the provider")
}

func TestStatusLinesNeverShareALineWithText(t *testing.T) {
	out := newOutput()
	f := &Frontend{out: out, atLineStart: true}
	sendErr := errors.New("failed to send prompt: boom")

	for _, ev := range []event.Event{
		event.TextDelta{Text: "partial"},
		event.Notice{Text: "retrying"},
		event.TextDelta{Text: "more"},
		event.ThinkingDelta{Text: "hidden"},
		event.ToolStart{Name: "Read", Args: `{"file_path":"` + strings.Repeat("x", 500) + `"}`},
		event.ToolEnd{Name: "Read", Output: "first\nsecond", IsError: true},
		event.TextDelta{Text: "answer"},
		event.TurnEnd{Outcome: event.Outcome{Status: event.Completed}},
		event.Error{Err: sendErr},
		event.TurnEnd{Outcome: event.Outcome{Status: event.Failed, Message: sendErr.Error()}},
	} {
		f.render(ev)
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 7, out.String())
	assert.Equal(t, []string{"partial", "retrying", "more"}, lines[:3])
	assert.True(t, strings.HasPrefix(lines[3], `[tool] Read {"file_path":"xxx`))
	assert.True(t, strings.HasSuffix(lines[3], "..."))
	assert.LessOrEqual(t, len(lines[3]), len("[tool] Read ")+maxArgsWidth)
	assert.Equal(t, []string{"[tool] Read error: first", "answer", "[error] failed to send prompt: boom"}, lines[4:])
}
