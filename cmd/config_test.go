package cmd

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/config"
	"github.com/keyute/kopi/internal/pigruntime/pigruntimetest"
)

func TestConfigShowsOrigins(t *testing.T) {
	e := newTestEnv(t)
	configFile := filepath.Join(e.kopiHome, "config.json")
	e.writeFile(t, configFile, `{"model":"global/model-1"}`)
	e.writeFile(t, filepath.Join(e.agentDir, "auth.json"), `{"marker":{"type":"api_key","key":"AUTH_MARKER_c41f"}}`)
	before := pigruntimetest.Snapshot(t, e.root)

	stdout, _, err := execute(t, "", "config")
	require.NoError(t, err)
	assert.Contains(t, stdout, e.kopiHome)
	assert.Contains(t, stdout, configFile)
	assert.Regexp(t, `version\s+= 1\s+\(default\)`, stdout)
	assert.Regexp(t, `model\s+= global/model-1\s+`+regexp.QuoteMeta("(global ("+configFile+"))"), stdout)

	stdout, _, err = execute(t, "", "config", "--model", "flag/model-2")
	require.NoError(t, err)
	assert.Regexp(t, `model\s+= flag/model-2\s+\(flags\)`, stdout)
	assert.NotContains(t, stdout, "AUTH_MARKER_c41f")
	assert.Equal(t, before, pigruntimetest.Snapshot(t, e.root))
}

func TestConfigRejectsUnknownKeyNamingTheLayer(t *testing.T) {
	e := newTestEnv(t)
	configFile := filepath.Join(e.kopiHome, "config.json")
	e.writeFile(t, configFile, `{"version":1,"bogus":true}`)

	_, _, err := execute(t, "", "config")

	require.ErrorIs(t, err, config.ErrUnknownKey)
	assert.Contains(t, err.Error(), "global ("+configFile+")")
}
