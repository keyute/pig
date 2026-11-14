package pigruntime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

const execOutput = "exec-output-91c2"

func callTool(name, id string) ai.FauxResponseStep {
	return ai.FauxAssistantMessage(
		ai.FauxContentBlocks{ai.FauxToolCall(name, map[string]any{}, &ai.FauxToolCallOptions{ID: id})},
		ai.FauxAssistantMessageOptions{StopReason: string(ai.StopReasonToolUse)},
	)
}

func execTool(ran *atomic.Int32) tool.Tool {
	return tool.Tool{
		Name:        "Exec",
		Label:       "Exec",
		Description: "Test-only tool that needs execute mode.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Capability:  policy.Exec,
		Run: func(context.Context, json.RawMessage) (string, error) {
			ran.Add(1)
			return execOutput, nil
		},
	}
}

func lastToolResult(t *testing.T, msgs []ai.Message) string {
	t.Helper()
	texts := toolResultTexts(msgs)
	require.NotEmpty(t, texts)
	return texts[len(texts)-1]
}

func TestHookBlocksToolUnknownToGate(t *testing.T) {
	sb := newSandbox(t)
	r, faux := newTestRuntime(t, sb)
	requests := make(chan []ai.Message, 1)
	faux.SetResponses([]ai.FauxResponseStep{callProbe(), captureRequest(requests, "ok")})

	var ran atomic.Bool
	probe := probeTool(func(context.Context, json.RawMessage) (string, error) {
		ran.Store(true)
		return toolOutput, nil
	})
	authority := policy.NewAuthority()
	sess, err := r.open(t.Context(), OpenOptions{Model: fauxModel, Tools: []tool.Tool{probe}, Authority: authority}, policy.NewGate(authority, nil))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sess.Close(context.Background())) })

	_, err = sess.Send(t.Context(), "probe")
	require.NoError(t, err)
	assert.Contains(t, lastToolResult(t, <-requests), policy.ErrUnknownTool.Error())
	assert.False(t, ran.Load())
}

func TestExecToolRunsOnlyInExecuteMode(t *testing.T) {
	sb := newSandbox(t)
	r, faux := newTestRuntime(t, sb)
	requests := make(chan []ai.Message, 1)
	faux.SetResponses([]ai.FauxResponseStep{
		callTool("Exec", "call-plan"), captureRequest(requests, "denied"),
		callTool("Exec", "call-exec"), captureRequest(requests, "ran"),
	})

	var ran atomic.Int32
	authority := policy.NewAuthority()
	sess, err := r.Open(t.Context(), OpenOptions{Model: fauxModel, Tools: []tool.Tool{execTool(&ran)}, Authority: authority})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sess.Close(context.Background())) })

	_, err = sess.Send(t.Context(), "run it")
	require.NoError(t, err)
	denied := lastToolResult(t, <-requests)
	assert.Contains(t, denied, policy.ErrDenied.Error())
	assert.Contains(t, denied, "plan mode")
	assert.Zero(t, ran.Load())

	authority.EnterExecute()
	_, err = sess.Send(t.Context(), "run it now")
	require.NoError(t, err)
	assert.Equal(t, execOutput, lastToolResult(t, <-requests))
	assert.Equal(t, int32(1), ran.Load())
}

func TestExecuteRefusesCallRevokedAfterBefore(t *testing.T) {
	var ran atomic.Int32
	authority := policy.NewAuthority()
	authority.EnterExecute()
	gate := policy.NewGate(authority, map[string]policy.Capability{"Exec": policy.Exec})
	require.NoError(t, gate.Before("call-1", "Exec"))
	authority.EnterPlan()

	res, err := agentTool{tool: execTool(&ran), gate: gate}.Execute(t.Context(), "call-1", json.RawMessage(`{}`), nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultContentText(res.Content), policy.ErrRevoked.Error())
	assert.Zero(t, ran.Load())
}
