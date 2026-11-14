package pigruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"

	"github.com/keyute/kopi/internal/policy"
	"github.com/keyute/kopi/internal/tool"
)

// pigSettings is unmarshalled rather than built because the nested settings
// types live in a PiG internal package and cannot be named here.
const pigSettings = `{"retry":{"enabled":true},"compaction":{"enabled":true},"cacheWarming":"off"}`

type Options struct {
	AgentDir, CWD string
}

type Runtime struct {
	svcs *coding.AgentSessionServices
	rt   *coding.Runtime
}

// New builds a PiG runtime whose every input is explicit: in-memory settings,
// untrusted project, and (in Open) no resources, no built-in tools. Leaving
// any of these nil makes PiG read ~/.pig, project settings or AGENTS.md.
func New(ctx context.Context, opts Options) (*Runtime, error) {
	var s coding.Settings
	if err := json.Unmarshal([]byte(pigSettings), &s); err != nil {
		return nil, fmt.Errorf("failed to decode pig settings: %w", err)
	}
	svcs, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{
		CWD:                opts.CWD,
		AgentDir:           opts.AgentDir,
		ProjectTrusted:     new(false),
		SettingsManager:    coding.NewInMemorySettingsManager(s),
		ModelRuntimeSignal: ctx,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create pig services: %w", err)
	}
	rt, err := coding.NewRuntime(coding.RuntimeOptions{Services: svcs})
	if err != nil {
		svcs.Close()
		return nil, fmt.Errorf("failed to create pig runtime: %w", err)
	}
	return &Runtime{svcs: svcs, rt: rt}, nil
}

func (r *Runtime) RegisterProvider(p *ai.ModelsProvider) error {
	if err := r.svcs.ModelRuntime().RegisterNativeProvider(p); err != nil {
		return fmt.Errorf("failed to register provider: %w", err)
	}
	return nil
}

type SessionInfo struct {
	ID           string
	Modified     time.Time
	FirstMessage string
}

// Sessions lists the sessions of the runtime's workspace, newest first.
func (r *Runtime) Sessions() ([]SessionInfo, error) {
	infos, err := r.rt.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	out := make([]SessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, SessionInfo{ID: info.ID, Modified: info.Modified, FirstMessage: info.FirstMessage})
	}
	return out, nil
}

type OpenOptions struct {
	// Model is "provider/id". It is used for a resumed session too, whatever
	// model the session was saved with.
	Model string
	// ResumeID resumes the session with exactly this id.
	ResumeID string
	// Continue resumes the most recent session of the workspace.
	Continue     bool
	SystemPrompt string
	Tools        []tool.Tool
	Authority    *policy.Authority
}

func (r *Runtime) Open(ctx context.Context, o OpenOptions) (*Session, error) {
	caps := make(map[string]policy.Capability, len(o.Tools))
	for _, t := range o.Tools {
		caps[t.Name] = t.Capability
	}
	return r.open(ctx, o, policy.NewGate(o.Authority, caps))
}

// open takes the gate separately so a test can install one that does not
// know every tool.
func (r *Runtime) open(ctx context.Context, o OpenOptions, gate *policy.Gate) (*Session, error) {
	if o.ResumeID != "" && o.Continue {
		return nil, errors.New("failed to open session: resume id and continue are mutually exclusive")
	}
	provider, id, _ := strings.Cut(o.Model, "/")
	model := r.svcs.ModelRuntime().GetModel(provider, id)
	if model == nil {
		return nil, fmt.Errorf("failed to open session with model %q: %w", o.Model, ErrUnknownModel)
	}
	ok, err := r.hasCredential(ctx, provider)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("failed to open session: provider %q: %w", provider, ErrNoCredential)
	}
	tools := make([]agent.AgentTool, 0, len(o.Tools))
	names := make([]string, 0, len(o.Tools))
	allowed := make(map[string]struct{}, len(o.Tools))
	for _, t := range o.Tools {
		tools = append(tools, agentTool{tool: t, gate: gate})
		names = append(names, t.Name)
		allowed[t.Name] = struct{}{}
	}
	// InitialActiveToolNames must be set too: nil falls back to PiG's
	// read/bash/edit/write defaults. An explicit Model also stops a resumed
	// session from restoring the model it was saved with.
	start := coding.SessionStartOptions{
		Model:                  model,
		SystemPrompt:           o.SystemPrompt,
		ResourceLoader:         coding.NoResources,
		SkipBuiltinTools:       true,
		ExtraTools:             tools,
		AllowedTools:           allowed,
		InitialActiveToolNames: names,
		BeforeToolCall:         []agent.BeforeToolCallHook{beforeToolCall(gate)},
	}
	var sess *coding.Session
	switch {
	case o.ResumeID != "":
		sess, err = r.resume(o.ResumeID, start)
	case o.Continue:
		sess, err = r.continueLatest(start)
	default:
		sess, err = r.rt.New(start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open session: %w", err)
	}
	return &Session{inner: sess}, nil
}

// resume and continueLatest check the listing first because PiG reports a
// missing session only as unwrapped error text.
func (r *Runtime) resume(id string, start coding.SessionStartOptions) (*coding.Session, error) {
	infos, err := r.Sessions()
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(infos, func(info SessionInfo) bool { return info.ID == id }) {
		return nil, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	return r.rt.Resume(id, start)
}

func (r *Runtime) continueLatest(start coding.SessionStartOptions) (*coding.Session, error) {
	infos, err := r.Sessions()
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("no session to continue: %w", ErrSessionNotFound)
	}
	return r.rt.Continue(start)
}

// hasCredential relies on PiG's CheckAuth returning (nil, nil) when the
// provider has no usable credential.
func (r *Runtime) hasCredential(ctx context.Context, provider string) (bool, error) {
	check, err := r.svcs.ModelRuntime().CheckAuth(ctx, provider)
	if err != nil {
		return false, fmt.Errorf("failed to check credential for provider %q: %w", provider, err)
	}
	return check != nil, nil
}

// Close must follow Close of every Session opened on r.
func (r *Runtime) Close() {
	if err := r.rt.Close(); err != nil {
		slog.Warn("failed to close pig runtime", "error", err)
	}
	r.svcs.Close()
}
