package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/pigruntime"
)

func TestLoginWithoutProviderListsProviders(t *testing.T) {
	newTestEnv(t)

	stdout, _, err := execute(t, "", "login")

	require.NoError(t, err)
	assert.Contains(t, stdout, "openai-codex")
	assert.Contains(t, stdout, "kopi login <provider>")
	assert.NotContains(t, stdout, "  openai\n", "openai's login needs a device ID Kopi does not keep")
}

func TestLoginOpenAIIsUnsupported(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "", "login", "openai")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support this provider's login yet")
}

func TestLoginUnknownProviderFails(t *testing.T) {
	newTestEnv(t)

	_, _, err := execute(t, "", "login", "nosuchprovider")

	require.ErrorIs(t, err, pigruntime.ErrUnknownProvider)
}
