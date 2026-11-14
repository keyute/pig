package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/config"
	"github.com/keyute/kopi/internal/pigruntime"
	"github.com/keyute/kopi/internal/pigruntime/pigruntimetest"
	"github.com/keyute/kopi/internal/tool"
)

const (
	// syntheticKey is the mock provider's API key; it must never be printed.
	syntheticKey = "sk-kopi-synthetic-3f9a"
	// waitLimit only guards against a hung test; no test relies on it elapsing.
	waitLimit = 30 * time.Second
)

type testEnv struct {
	root, home, kopiHome, agentDir, workspace string
}

// newTestEnv points HOME and KOPI_HOME at empty temp dirs, runs in a temp
// workspace with an empty PATH, and restores the PiG variables the commands
// set process-wide.
func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	root := t.TempDir()
	e := testEnv{
		root:      root,
		home:      filepath.Join(root, "home"),
		kopiHome:  filepath.Join(root, "kopi"),
		workspace: filepath.Join(root, "ws"),
	}
	e.agentDir = filepath.Join(e.kopiHome, "agent")
	for _, dir := range []string{e.home, e.kopiHome, e.workspace} {
		require.NoError(t, os.Mkdir(dir, 0o755))
	}
	t.Setenv("HOME", e.home)
	t.Setenv("KOPI_HOME", e.kopiHome)
	t.Setenv("PATH", "")
	set, unset := pigruntime.IsolationEnv(e.kopiHome, e.agentDir)
	for k := range set {
		t.Setenv(k, os.Getenv(k))
	}
	for _, k := range unset {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
	t.Chdir(e.workspace)
	return e
}

func (e testEnv) writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// execute runs the command tree in-process with stdin as input.
func execute(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRoot()
	var out, errOut bytes.Buffer
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

func TestHelpVersionAndConfigCreateNothing(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"version"}, {"config"}} {
		t.Run(args[0], func(t *testing.T) {
			e := newTestEnv(t)
			before := pigruntimetest.Snapshot(t, e.root)

			stdout, _, err := execute(t, "", args...)

			require.NoError(t, err)
			assert.NotEmpty(t, stdout)
			assert.Equal(t, before, pigruntimetest.Snapshot(t, e.root))
			assert.NoDirExists(t, e.agentDir)
		})
	}
}

func TestRootWithoutModelFails(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "")

	require.ErrorIs(t, err, config.ErrNoModel)
	assert.Contains(t, err.Error(), "--model")
}

func TestRootRejectsContinueWithResume(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "", "--model", "a/b", "--continue", "--resume", "x")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "continue")
	assert.Contains(t, err.Error(), "resume")
}

func TestRootRejectsInvalidLogLevel(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "", "--log-level", "loud", "config")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--log-level")
}

// builtinModel names one of PiG's builtin models for provider.
func builtinModel(t *testing.T, provider string) string {
	t.Helper()
	models := ai.GetBuiltinModels(provider)
	require.NotEmpty(t, models)
	return provider + "/" + models[0].ID
}

// openai-codex is OAuth-only, so with an empty home it has no credential;
// Open fails at the credential check, before any provider request.
func TestRootWithoutCredentialSuggestsLogin(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "", "--model", builtinModel(t, "openai-codex"))

	require.Error(t, err)
	assert.Equal(t, `no credential for provider "openai-codex": run "kopi login openai-codex"`, err.Error())
}

func TestRootWithoutCredentialForProviderWithoutLogin(t *testing.T) {
	newTestEnv(t)
	t.Setenv("MISTRAL_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	for _, provider := range []string{"mistral", "openai"} {
		_, _, err := execute(t, "", "--model", builtinModel(t, provider))

		require.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("no credential for provider %q", provider))
		assert.Contains(t, err.Error(), "API key in the environment")
		assert.NotContains(t, err.Error(), "login")
	}
}

func TestRootUnknownModelNamesWhereItIsSet(t *testing.T) {
	e := newTestEnv(t)

	_, _, err := execute(t, "", "--model", "nosuch/model-1")
	require.Error(t, err)
	assert.Equal(t, `unknown model "nosuch/model-1": check --model`, err.Error())

	e.writeFile(t, filepath.Join(e.kopiHome, "config.json"), `{"model":"nosuch/model-1"}`)
	_, _, err = execute(t, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(e.kopiHome, "config.json"))
}

func TestRootReportsMissingSession(t *testing.T) {
	e := newTestEnv(t)
	server := newMockProvider(t, func(w http.ResponseWriter) { streamText(w, "unused") })
	e.writeMockModels(t, server.URL)

	_, _, err := execute(t, "", "--model", mockModel, "--resume", "no-such-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no session "no-such-id"`)

	_, _, err = execute(t, "", "--model", mockModel, "--continue")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session to continue")
}

const mockModel = "kopi-mock/mock-1"

// writeMockModels configures a custom OpenAI-compatible provider in PiG's
// models.json under Kopi's agent dir. The apiKey is a literal: PiG treats a
// value without "$" or a leading "!" as the key itself.
func (e testEnv) writeMockModels(t *testing.T, baseURL string) {
	t.Helper()
	models := map[string]any{"providers": map[string]any{"kopi-mock": map[string]any{
		"baseUrl": baseURL + "/v1",
		"api":     "openai-completions",
		"apiKey":  syntheticKey,
		"models":  []any{map[string]any{"id": "mock-1", "name": "Kopi mock"}},
	}}}
	b, err := json.Marshal(models)
	require.NoError(t, err)
	e.writeFile(t, filepath.Join(e.agentDir, "models.json"), string(b))
}

type mockRequest struct {
	path, authorization string
	body                map[string]any
}

// newMockProvider serves chat completions with respond and records each
// request.
func newMockProvider(t *testing.T, respond func(http.ResponseWriter)) *mockServer {
	t.Helper()
	m := &mockServer{requests: make(chan mockRequest, 16)}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		m.requests <- mockRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), body: body}
		respond(w)
	}))
	t.Cleanup(m.Close)
	return m
}

type mockServer struct {
	*httptest.Server
	requests chan mockRequest
}

func (m *mockServer) next(t *testing.T) mockRequest {
	t.Helper()
	select {
	case req := <-m.requests:
		return req
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for a provider request")
		return mockRequest{}
	}
}

// writeChunk writes one OpenAI chat-completions streaming chunk.
func writeChunk(w http.ResponseWriter, delta map[string]any, finish any) {
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1, "model": "mock-1",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// streamText answers in the OpenAI chat-completions streaming format.
func streamText(w http.ResponseWriter, parts ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	writeChunk(w, map[string]any{"role": "assistant", "content": ""}, nil)
	for _, p := range parts {
		writeChunk(w, map[string]any{"content": p}, nil)
	}
	writeChunk(w, map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// streamReads answers with one Read tool call per path.
func streamReads(w http.ResponseWriter, paths ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	writeChunk(w, map[string]any{"role": "assistant", "content": ""}, nil)
	for i, p := range paths {
		args, _ := json.Marshal(map[string]any{"file_path": p})
		writeChunk(w, map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": fmt.Sprintf("call_%d", i), "type": "function",
			"function": map[string]any{"name": "Read", "arguments": string(args)},
		}}}, nil)
	}
	writeChunk(w, map[string]any{}, "tool_calls")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestRootStreamsTurnFromMockServer(t *testing.T) {
	e := newTestEnv(t)
	server := newMockProvider(t, func(w http.ResponseWriter) { streamText(w, "Hello from ", "the mock server.") })
	e.writeMockModels(t, server.URL)

	stdout, stderr, err := execute(t, "say hello\n/quit\n", "--model", mockModel)

	require.NoError(t, err)
	assert.Contains(t, stdout, "[session] ")
	assert.Contains(t, stdout, "Hello from the mock server.\n")
	req := server.next(t)
	assert.Equal(t, "/v1/chat/completions", req.path)
	assert.Equal(t, "Bearer "+syntheticKey, req.authorization)
	var tools []string
	for _, tl := range req.body["tools"].([]any) {
		tools = append(tools, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"Read"}, tools)
	messages := req.body["messages"].([]any)
	first := messages[0].(map[string]any)
	assert.Contains(t, []string{"system", "developer"}, first["role"])
	assert.Contains(t, first["content"], "You are Kopi, a coding agent working in")
	assert.NotContains(t, stdout+stderr, syntheticKey)
}

func TestRootRendersProviderFailure(t *testing.T) {
	e := newTestEnv(t)
	server := newMockProvider(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"mock rejected the request","type":"invalid_request_error"}}`)
	})
	e.writeMockModels(t, server.URL)

	stdout, stderr, err := execute(t, "say hello\n/status\n/quit\n", "--model", mockModel)

	require.NoError(t, err)
	server.next(t)
	assert.Contains(t, stdout, "[error] ")
	assert.Contains(t, stdout, "mock rejected the request")
	assert.Contains(t, stdout, "turn: idle", "the session carries on after the failure")
	assert.NotContains(t, stdout+stderr, syntheticKey)
}

// The workspace spans HOME and KOPI_HOME, so only the protected list, not the
// workspace boundary, can refuse these reads.
func TestRootReadRefusesProtectedPaths(t *testing.T) {
	e := newTestEnv(t)
	t.Chdir(e.root)
	secret := filepath.Join(e.kopiHome, "secret.txt")
	key := filepath.Join(e.home, ".ssh", "id_x")
	e.writeFile(t, secret, "synthetic kopi secret")
	e.writeFile(t, key, "synthetic private key")
	var calls atomic.Int32
	server := newMockProvider(t, func(w http.ResponseWriter) {
		if calls.Add(1) == 1 {
			streamReads(w, secret, key)
			return
		}
		streamText(w, "done")
	})
	e.writeMockModels(t, server.URL)

	_, _, err := execute(t, "read them\n/quit\n", "--model", mockModel)

	require.NoError(t, err)
	server.next(t)
	var results []string
	for _, m := range server.next(t).body["messages"].([]any) {
		if msg := m.(map[string]any); msg["role"] == "tool" {
			results = append(results, fmt.Sprint(msg["content"]))
		}
	}
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Contains(t, r, tool.ErrProtected.Error())
		assert.NotContains(t, r, "synthetic")
	}
}
