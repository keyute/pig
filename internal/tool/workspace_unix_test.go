//go:build unix

package tool

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenRefusesFIFOWithoutBlocking(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	require.NoError(t, syscall.Mkfifo(filepath.Join(home, "pipe"), 0o600))
	require.NoError(t, syscall.Mkfifo(filepath.Join(home, ".ssh", "pipe"), 0o600))
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	for _, tc := range []struct {
		path string
		want error
	}{
		{"pipe", ErrNotRegular},
		{".ssh/pipe", ErrProtected},
	} {
		t.Run(tc.path, func(t *testing.T) {
			// A blocking open of a writer-less FIFO never returns, so a
			// regression fails here through go test's -timeout.
			f, err := ws.Open(tc.path)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, f)
		})
	}
}

func TestOpenFailsClosedWhenProtectedPathCannotBeChecked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	locked := filepath.Join(t.TempDir(), "locked")
	writeFile(t, filepath.Join(locked, ".ssh", "id_x"), "private key")
	ws := newWorkspace(t, dir, []string{filepath.Join(locked, ".ssh")})
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { require.NoError(t, os.Chmod(locked, 0o755)) })

	for _, path := range []string{"a.txt", "missing.txt"} {
		f, err := ws.Open(path)
		require.ErrorIs(t, err, os.ErrPermission, path)
		require.Nil(t, f)
	}
}
