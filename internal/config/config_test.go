package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func globalLayer(t *testing.T, content string) Layer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	l, err := GlobalFile(path)
	require.NoError(t, err)
	return l
}

func TestResolveDefaultsOnly(t *testing.T) {
	r, err := Resolve(Defaults())
	require.NoError(t, err)
	assert.Equal(t, Config{Version: 1}, r.Config)
	assert.Equal(t, map[string]string{"version": "default"}, r.Origins)
}

func TestResolveLaterLayersOverrideEarlier(t *testing.T) {
	global := globalLayer(t, `{"version": 1, "model": "openai-codex/gpt-x"}`)
	r, err := Resolve(Defaults(), global, Map("flags", map[string]any{"model": "anthropic/claude"}))
	require.NoError(t, err)
	assert.Equal(t, Config{Version: 1, Model: "anthropic/claude"}, r.Config)
	assert.Equal(t, map[string]string{"version": global.Name, "model": "flags"}, r.Origins)
}

func TestResolveLayerWithoutKeyKeepsEarlierOrigin(t *testing.T) {
	global := globalLayer(t, `{"model": "openai-codex/gpt-x"}`)
	r, err := Resolve(Defaults(), global, Map("flags", nil))
	require.NoError(t, err)
	assert.Equal(t, Config{Version: 1, Model: "openai-codex/gpt-x"}, r.Config)
	assert.Equal(t, map[string]string{"version": "default", "model": global.Name}, r.Origins)
}

func TestResolveMissingGlobalFileYieldsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "config.json")
	global, err := GlobalFile(path)
	require.NoError(t, err)
	assert.Contains(t, global.Name, path)

	r, err := Resolve(Defaults(), global)
	require.NoError(t, err)
	assert.Equal(t, Config{Version: 1}, r.Config)
	assert.Equal(t, map[string]string{"version": "default"}, r.Origins)
}

func TestResolveRejectsUnknownKeyNamingLayer(t *testing.T) {
	global := globalLayer(t, `{"version": 1, "modle": "a/b"}`)
	_, err := Resolve(Defaults(), global)
	require.ErrorIs(t, err, ErrUnknownKey)
	assert.Contains(t, err.Error(), global.Name)
	assert.Contains(t, err.Error(), "modle")
}

func TestResolveRejectsWrongTypeNamingLayer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"string version", `{"version": "1"}`},
		{"numeric model", `{"model": 5}`},
		{"fractional version", `{"version": 1.9}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := globalLayer(t, tc.content)
			_, err := Resolve(Defaults(), global)
			require.ErrorIs(t, err, ErrInvalidValue)
			assert.NotErrorIs(t, err, ErrUnknownKey)
			assert.Contains(t, err.Error(), global.Name)
		})
	}
}

func TestResolveRejectsMalformedJSONNamingLayer(t *testing.T) {
	global := globalLayer(t, `{"version": 1,`)
	_, err := Resolve(Defaults(), global)
	require.ErrorIs(t, err, ErrInvalidValue)
	assert.Contains(t, err.Error(), global.Name)
}

func TestResolveRejectsUnsupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{"newer", 2},
		{"explicit zero", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Defaults(), Map("flags", map[string]any{"version": tc.version}))
			require.ErrorIs(t, err, ErrUnsupportedVersion)
			assert.Contains(t, err.Error(), "flags")
		})
	}
}

func TestResolveRejectsInvalidModelNamingLayer(t *testing.T) {
	_, err := Resolve(Defaults(), Map("flags", map[string]any{"model": "noslash"}))
	require.ErrorIs(t, err, ErrInvalidModel)
	assert.Contains(t, err.Error(), "flags")
}

func TestEntriesSortedWithUnsetKeysAndOrigins(t *testing.T) {
	r, err := Resolve(Defaults())
	require.NoError(t, err)
	assert.Equal(t, []Entry{
		{Key: "model", Value: "", Origin: "unset"},
		{Key: "version", Value: "1", Origin: "default"},
	}, r.Entries())

	r, err = Resolve(Defaults(), Map("flags", map[string]any{"model": "a/b"}))
	require.NoError(t, err)
	assert.Equal(t, []Entry{
		{Key: "model", Value: "a/b", Origin: "flags"},
		{Key: "version", Value: "1", Origin: "default"},
	}, r.Entries())
}

func TestParseModelRef(t *testing.T) {
	for _, tc := range []struct {
		in, provider, id string
		wantErr          bool
	}{
		{in: "openai-codex/gpt-x", provider: "openai-codex", id: "gpt-x"},
		{in: "a/b/c", provider: "a", id: "b/c"},
		{in: "noslash", wantErr: true},
		{in: "/id", wantErr: true},
		{in: "provider/", wantErr: true},
		{in: "", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			provider, id, err := ParseModelRef(tc.in)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidModel)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.provider, provider)
			assert.Equal(t, tc.id, id)
		})
	}
}
