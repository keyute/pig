package policy

import "sync"

type Capability string

const (
	Read  Capability = "read"
	Write Capability = "write"
	Exec  Capability = "exec"
)

// NeedsExecute fails closed: any capability other than Read requires execute mode.
func (c Capability) NeedsExecute() bool {
	return c != Read
}

type Mode int

const (
	Plan Mode = iota
	Execute
)

func (m Mode) String() string {
	if m == Execute {
		return "execute"
	}
	return "plan"
}

// Authority holds the session mode. The generation increases on every actual
// mode change so grants issued under an earlier mode can be recognised as stale.
type Authority struct {
	mu         sync.Mutex
	mode       Mode
	generation uint64
}

func NewAuthority() *Authority {
	return &Authority{mode: Plan, generation: 1}
}

func (a *Authority) Snapshot() (Mode, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode, a.generation
}

func (a *Authority) EnterExecute() uint64 {
	return a.enter(Execute)
}

func (a *Authority) EnterPlan() uint64 {
	return a.enter(Plan)
}

func (a *Authority) enter(m Mode) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mode != m {
		a.mode = m
		a.generation++
	}
	return a.generation
}

func (a *Authority) Permits(c Capability) (uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generation, !c.NeedsExecute() || a.mode == Execute
}
