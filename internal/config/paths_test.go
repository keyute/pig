package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePathsPrefersKopiHome(t *testing.T) {
	kopiHome := filepath.Join(t.TempDir(), "kopi")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KOPI_HOME", kopiHome)

	p, err := ResolvePaths()
	require.NoError(t, err)
	assert.Equal(t, Paths{
		Home:       kopiHome,
		ConfigFile: filepath.Join(kopiHome, "config.json"),
		AgentDir:   filepath.Join(kopiHome, "agent"),
	}, p)
	assert.NoDirExists(t, kopiHome)
}

func TestResolvePathsMakesRelativeKopiHomeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("KOPI_HOME", "rel/kopi")

	p, err := ResolvePaths()
	require.NoError(t, err)
	wd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(wd, "rel", "kopi"), p.Home)
	assert.NoDirExists(t, p.Home)
}

func TestResolvePathsDefaultsUnderUserHome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset bool
	}{
		{"unset", true},
		{"empty", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("KOPI_HOME", "") // registers restoration of the original value
			if tc.unset {
				require.NoError(t, os.Unsetenv("KOPI_HOME"))
			}

			p, err := ResolvePaths()
			require.NoError(t, err)
			want := filepath.Join(home, ".kopi")
			assert.Equal(t, Paths{
				Home:       want,
				ConfigFile: filepath.Join(want, "config.json"),
				AgentDir:   filepath.Join(want, "agent"),
			}, p)
			assert.NoDirExists(t, want)
		})
	}
}
