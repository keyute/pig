package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGate() (*Authority, *Gate) {
	a := NewAuthority()
	return a, NewGate(a, map[string]Capability{"Read": Read, "Bash": Exec})
}

func TestUnknownToolIsDenied(t *testing.T) {
	_, g := newTestGate()
	assert.ErrorIs(t, g.Before("c1", "Nope"), ErrUnknownTool)
}

func TestReadIsAllowedInBothModes(t *testing.T) {
	a, g := newTestGate()
	require.NoError(t, g.Before("c1", "Read"))
	require.NoError(t, g.Admit("c1", "Read"))
	a.EnterExecute()
	require.NoError(t, g.Before("c2", "Read"))
	require.NoError(t, g.Admit("c2", "Read"))
}

func TestReadGrantSurvivesModeChanges(t *testing.T) {
	a, g := newTestGate()
	require.NoError(t, g.Before("c1", "Read"))
	a.EnterExecute()
	a.EnterPlan()
	assert.NoError(t, g.Admit("c1", "Read"))
}

func TestExecIsDeniedInPlan(t *testing.T) {
	_, g := newTestGate()
	err := g.Before("c1", "Bash")
	require.ErrorIs(t, err, ErrDenied)
	assert.Contains(t, err.Error(), `tool "Bash" needs execute mode; the session is in plan mode`)
}

func TestExecIsAdmittedInExecute(t *testing.T) {
	a, g := newTestGate()
	a.EnterExecute()
	require.NoError(t, g.Before("c1", "Bash"))
	assert.NoError(t, g.Admit("c1", "Bash"))
}

func TestExecGrantIsRevokedOnReturnToPlan(t *testing.T) {
	a, g := newTestGate()
	a.EnterExecute()
	require.NoError(t, g.Before("c1", "Bash"))
	a.EnterPlan()
	assert.ErrorIs(t, g.Admit("c1", "Bash"), ErrRevoked)
}

func TestStaleExecGrantIsRevokedAfterReturningToExecute(t *testing.T) {
	a, g := newTestGate()
	a.EnterExecute()
	require.NoError(t, g.Before("c1", "Bash"))
	a.EnterPlan()
	a.EnterExecute()
	assert.ErrorIs(t, g.Admit("c1", "Bash"), ErrRevoked)
}

func TestAdmitWithoutBeforeHasNoGrant(t *testing.T) {
	_, g := newTestGate()
	assert.ErrorIs(t, g.Admit("c1", "Read"), ErrNoGrant)
}

func TestGrantIsConsumedOnce(t *testing.T) {
	_, g := newTestGate()
	require.NoError(t, g.Before("c1", "Read"))
	require.NoError(t, g.Admit("c1", "Read"))
	assert.ErrorIs(t, g.Admit("c1", "Read"), ErrNoGrant)
}

func TestGrantForOneToolIsNotAdmittedForAnother(t *testing.T) {
	a, g := newTestGate()
	a.EnterExecute()
	require.NoError(t, g.Before("c1", "Read"))
	assert.ErrorIs(t, g.Admit("c1", "Bash"), ErrNoGrant)
}

func TestNewGateCopiesCapabilities(t *testing.T) {
	caps := map[string]Capability{"Read": Read}
	g := NewGate(NewAuthority(), caps)
	caps["Read"] = Exec
	caps["Bash"] = Read
	assert.NoError(t, g.Before("c1", "Read"))
	assert.ErrorIs(t, g.Before("c2", "Bash"), ErrUnknownTool)
}
