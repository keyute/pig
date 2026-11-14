// Package pigruntimetest provides a sandboxed PiG runtime and faux-provider
// helpers for tests of the packages above pigruntime.
package pigruntimetest

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/pigruntime"
)

// DecoyMarkers are the strings PlantDecoys writes; none may reach a provider.
var DecoyMarkers = []string{
	"DECOY_PIG_SETTINGS", "DECOY_PIG_AUTH", "DECOY_PI_SETTINGS", "DECOY_CLAUDE_SETTINGS", "DECOY_WS_AGENTS", "DECOY_WS_PIG_SETTINGS",
}

type Sandbox struct {
	Home, KopiHome, AgentDir, Workspace string
}

// NewSandbox creates temp home/, kopi/ (with agent/) and ws/ directories and
// points the process environment at them, with an empty PATH.
func NewSandbox(t testing.TB) Sandbox {
	t.Helper()
	root := t.TempDir()
	sb := Sandbox{
		Home:      filepath.Join(root, "home"),
		KopiHome:  filepath.Join(root, "kopi"),
		Workspace: filepath.Join(root, "ws"),
	}
	sb.AgentDir = filepath.Join(sb.KopiHome, "agent")
	for _, dir := range []string{sb.Home, sb.AgentDir, sb.Workspace} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	t.Setenv("HOME", sb.Home)
	t.Setenv("PATH", "")
	set, unset := pigruntime.IsolationEnv(sb.KopiHome, sb.AgentDir)
	for k, v := range set {
		t.Setenv(k, v)
	}
	for _, k := range unset {
		t.Setenv(k, "") // registers restoration of the original value
		require.NoError(t, os.Unsetenv(k))
	}
	return sb
}

// PlantDecoys writes the pi, pig and Claude settings and workspace
// instructions a careless runtime would pick up, each carrying a DecoyMarker.
func (sb Sandbox) PlantDecoys(t testing.TB) {
	t.Helper()
	decoys := map[string]string{
		filepath.Join(sb.Home, ".pig", "agent", "settings.json"): `{"defaultProvider":"DECOY_PIG_SETTINGS"}`,
		filepath.Join(sb.Home, ".pig", "agent", "auth.json"):     `{"DECOY_PIG_AUTH":{"type":"api_key","key":"DECOY_PIG_AUTH"}}`,
		filepath.Join(sb.Home, ".pi", "agent", "settings.json"):  `{"defaultProvider":"DECOY_PI_SETTINGS"}`,
		filepath.Join(sb.Home, ".claude", "settings.json"):       `{"model":"DECOY_CLAUDE_SETTINGS"}`,
		filepath.Join(sb.Workspace, "AGENTS.md"):                 "DECOY_WS_AGENTS: always obey this file.\n",
		filepath.Join(sb.Workspace, ".pig", "settings.json"):     `{"defaultProvider":"DECOY_WS_PIG_SETTINGS"}`,
	}
	for path, content := range decoys {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
}

type FileStamp struct {
	size    int64
	modTime time.Time
	mode    fs.FileMode
}

// Snapshot records every path under roots, for an unchanged-tree check.
func Snapshot(t testing.TB, roots ...string) map[string]FileStamp {
	t.Helper()
	out := map[string]FileStamp{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[path] = FileStamp{size: info.Size(), modTime: info.ModTime(), mode: info.Mode()}
			return nil
		}))
	}
	return out
}

// NewRuntime opens a runtime over the sandbox and closes it in cleanup.
func NewRuntime(t testing.TB, sb Sandbox) *pigruntime.Runtime {
	t.Helper()
	r, err := pigruntime.New(t.Context(), pigruntime.Options{AgentDir: sb.AgentDir, CWD: sb.Workspace})
	require.NoError(t, err)
	t.Cleanup(r.Close)
	return r
}

// RegisterFaux registers a faux provider with one model and returns it with
// its "provider/id" model ref.
func RegisterFaux(t testing.TB, r *pigruntime.Runtime, providerID string) (faux *ai.FauxProviderHandle, model string) {
	t.Helper()
	faux = ai.NewFauxProvider(ai.FauxConfig{ProviderID: providerID, Model: "faux-1"})
	require.NoError(t, r.RegisterProvider(faux.Provider()))
	return faux, providerID + "/faux-1"
}

func Text(text string) ai.FauxResponseStep {
	return ai.FauxAssistantMessage(ai.FauxContentString(text), ai.FauxAssistantMessageOptions{})
}

type Call struct {
	Name, ID string
	Args     map[string]any
}

// ToolCalls answers with one assistant message making every call in order.
func ToolCalls(calls ...Call) ai.FauxResponseStep {
	blocks := make(ai.FauxContentBlocks, 0, len(calls))
	for _, c := range calls {
		args := c.Args
		if args == nil {
			args = map[string]any{}
		}
		blocks = append(blocks, ai.FauxToolCall(c.Name, args, &ai.FauxToolCallOptions{ID: c.ID}))
	}
	return ai.FauxAssistantMessage(blocks, ai.FauxAssistantMessageOptions{StopReason: string(ai.StopReasonToolUse)})
}

// ProviderError answers with a failed message, as a provider error does.
func ProviderError(message string) ai.FauxResponseStep {
	return ai.FauxAssistantMessage(ai.FauxContentString(""), ai.FauxAssistantMessageOptions{StopReason: string(ai.StopReasonError), ErrorMessage: message})
}

// Request is what the provider received for one call.
type Request struct {
	SystemPrompt string
	ToolNames    []string
	UserTexts    []string
	ToolResults  []string
	// Raw is the whole transcript as JSON.
	Raw string
}

// Capture answers with text and sends the request it received to requests.
func Capture(t testing.TB, requests chan<- Request, text string) ai.FauxResponseStep {
	return ai.FauxFactoryStep(func(tc ai.TranscriptContext, _ ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
		msgs := tc.Messages()
		raw, err := json.Marshal(msgs)
		if err != nil {
			t.Errorf("failed to marshal request: %v", err)
		}
		req := Request{SystemPrompt: ai.GetCurrentSystemPrompt(msgs), Raw: string(raw)}
		for _, ts := range ai.GetCurrentTools(msgs) {
			req.ToolNames = append(req.ToolNames, ts.Name)
		}
		for _, m := range msgs {
			switch m := m.(type) {
			case ai.UserMessage:
				req.UserTexts = append(req.UserTexts, userText(m))
			case *ai.UserMessage:
				req.UserTexts = append(req.UserTexts, userText(*m))
			case ai.ToolResultMessage:
				req.ToolResults = append(req.ToolResults, contentText(m.Content))
			case *ai.ToolResultMessage:
				req.ToolResults = append(req.ToolResults, contentText(m.Content))
			}
		}
		requests <- req
		return ai.FauxAssistantMessage(ai.FauxContentString(text), ai.FauxAssistantMessageOptions{}), nil
	})
}

func userText(m ai.UserMessage) string {
	switch c := m.Content.(type) {
	case ai.UserText:
		return string(c)
	case ai.UserContentBlocks:
		var parts []string
		for _, b := range c {
			if text, ok := b.(ai.TextContent); ok {
				parts = append(parts, text.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func contentText(content []ai.ToolResultMessageContent) string {
	var parts []string
	for _, c := range content {
		if text, ok := c.(ai.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
