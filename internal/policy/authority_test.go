package policy

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuthorityStartsInPlanAtGenerationOne(t *testing.T) {
	mode, gen := NewAuthority().Snapshot()
	assert.Equal(t, Plan, mode)
	assert.Equal(t, uint64(1), gen)
}

func TestModeChangesBumpGenerationOnce(t *testing.T) {
	a := NewAuthority()
	assert.Equal(t, uint64(2), a.EnterExecute())
	assert.Equal(t, uint64(3), a.EnterPlan())
	mode, gen := a.Snapshot()
	assert.Equal(t, Plan, mode)
	assert.Equal(t, uint64(3), gen)
}

func TestReenteringCurrentModeDoesNotBump(t *testing.T) {
	a := NewAuthority()
	assert.Equal(t, uint64(1), a.EnterPlan())
	assert.Equal(t, uint64(2), a.EnterExecute())
	assert.Equal(t, uint64(2), a.EnterExecute())
}

func TestPermitsByModeAndCapability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execute bool
		cap     Capability
		want    bool
	}{
		{"plan read", false, Read, true},
		{"plan write", false, Write, false},
		{"plan exec", false, Exec, false},
		{"plan unknown", false, Capability("network"), false},
		{"execute read", true, Read, true},
		{"execute write", true, Write, true},
		{"execute exec", true, Exec, true},
		{"execute unknown", true, Capability("network"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAuthority()
			if tc.execute {
				a.EnterExecute()
			}
			_, wantGen := a.Snapshot()
			gen, ok := a.Permits(tc.cap)
			assert.Equal(t, tc.want, ok)
			assert.Equal(t, wantGen, gen)
		})
	}
}

func TestConcurrentTransitionsAndPermitsAreRaceFree(t *testing.T) {
	a := NewAuthority()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				a.EnterExecute()
				a.EnterPlan()
			}
		})
		wg.Go(func() {
			for range 100 {
				a.Permits(Exec)
				a.Snapshot()
			}
		})
	}
	wg.Wait()
	mode, gen := a.Snapshot()
	assert.Equal(t, Plan, mode)
	assert.Equal(t, uint64(1), gen%2, "every completed execute/plan pair bumps twice")
}
