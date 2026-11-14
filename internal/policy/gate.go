package policy

import (
	"fmt"
	"maps"
	"sync"
)

type grant struct {
	tool       string
	generation uint64
}

// Gate checks each tool call twice: Before runs in the pre-dispatch hook for
// the whole batch, Admit runs when the call actually executes, so a mode change
// between the two is caught.
type Gate struct {
	authority *Authority
	caps      map[string]Capability
	mu        sync.Mutex
	grants    map[string]grant
}

func NewGate(a *Authority, caps map[string]Capability) *Gate {
	return &Gate{authority: a, caps: maps.Clone(caps), grants: map[string]grant{}}
}

func (g *Gate) Before(callID, tool string) error {
	c, ok := g.caps[tool]
	if !ok {
		return fmt.Errorf("tool %q: %w", tool, ErrUnknownTool)
	}
	gen, ok := g.authority.Permits(c)
	if !ok {
		// Only plan mode denies anything, so the mode is known without a racy re-read.
		return fmt.Errorf("tool %q needs execute mode; the session is in %s mode: %w", tool, Plan, ErrDenied)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants[callID] = grant{tool: tool, generation: gen}
	return nil
}

func (g *Gate) Admit(callID, tool string) error {
	g.mu.Lock()
	gr, ok := g.grants[callID]
	delete(g.grants, callID)
	g.mu.Unlock()
	if !ok || gr.tool != tool {
		return fmt.Errorf("tool %q: %w", tool, ErrNoGrant)
	}
	c := g.caps[tool]
	gen, ok := g.authority.Permits(c)
	if !ok || (c.NeedsExecute() && gen != gr.generation) {
		return fmt.Errorf("tool %q: the session mode changed after the call was approved: %w", tool, ErrRevoked)
	}
	return nil
}
