package tool

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func newWorkspace(t *testing.T, dir string, protected []string) *Workspace {
	t.Helper()
	ws, err := NewWorkspace(dir, protected)
	require.NoError(t, err)
	t.Cleanup(func() { ws.Close() })
	return ws
}

func readAll(t *testing.T, ws *Workspace, path string) string {
	t.Helper()
	f, err := ws.Open(path)
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}

func TestOpenReadsRelativePathInside(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub", "a.txt"), "hello")
	ws := newWorkspace(t, dir, nil)

	assert.Equal(t, "hello", readAll(t, ws, "sub/a.txt"))
}

func TestOpenReadsAbsolutePathInside(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	ws := newWorkspace(t, dir, nil)

	assert.Equal(t, "hello", readAll(t, ws, filepath.Join(dir, "a.txt")))
	assert.Equal(t, "hello", readAll(t, ws, filepath.Join(realDir, "a.txt")))
}

func TestOpenRefusesPathsOutsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ws")
	writeFile(t, filepath.Join(dir, "sub", "in.txt"), "in")
	writeFile(t, filepath.Join(parent, "out.txt"), "secret")
	require.NoError(t, os.Symlink(filepath.Join(parent, "out.txt"), filepath.Join(dir, "abs-link")))
	require.NoError(t, os.Symlink("../out.txt", filepath.Join(dir, "rel-link")))
	ws := newWorkspace(t, dir, nil)

	for _, tc := range []struct {
		name, path string
	}{
		{"dot-dot escape", "../out.txt"},
		{"nested dot-dot escape", "sub/../../out.txt"},
		{"absolute path outside", filepath.Join(parent, "out.txt")},
		{"absolute symlink outside", "abs-link"},
		{"relative symlink outside", "rel-link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ws.Open(tc.path)
			require.ErrorIs(t, err, ErrOutsideWorkspace)
			assert.Nil(t, f)
			assert.Contains(t, err.Error(), tc.path)
		})
	}
}

func TestOpenFollowsSymlinkStayingInside(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub", "a.txt"), "hello")
	require.NoError(t, os.Symlink("sub/a.txt", filepath.Join(dir, "link")))
	ws := newWorkspace(t, dir, nil)

	assert.Equal(t, "hello", readAll(t, ws, "link"))
}

// syntheticHome builds a fake home directory holding credential stores and a
// Kopi home, and returns the home and the Kopi home paths.
func syntheticHome(t *testing.T) (home, kopiHome string) {
	t.Helper()
	home = t.TempDir()
	kopiHome = filepath.Join(home, "kopi-home")
	writeFile(t, filepath.Join(home, ".ssh", "id_x"), "private key")
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "aws secret")
	writeFile(t, filepath.Join(home, ".claude.json"), "{}")
	writeFile(t, filepath.Join(kopiHome, "config.json"), "{}")
	writeFile(t, filepath.Join(home, ".env"), "TOKEN=1")
	return home, kopiHome
}

func TestOpenRefusesProtectedStoresInsideHomeWorkspace(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	for _, path := range []string{
		".ssh/id_x",
		".ssh",
		".aws/credentials",
		".claude.json",
		"kopi-home/config.json",
		filepath.Join(home, ".ssh", "id_x"),
	} {
		t.Run(path, func(t *testing.T) {
			f, err := ws.Open(path)
			require.ErrorIs(t, err, ErrProtected)
			assert.Nil(t, f)
			assert.Contains(t, err.Error(), path)
			assert.NotContains(t, err.Error(), "private key")
		})
	}
}

func TestOpenRefusesSymlinkedCredentialStore(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	require.NoError(t, os.Symlink(".ssh", filepath.Join(home, "link")))
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	_, err := ws.Open("link/id_x")
	require.ErrorIs(t, err, ErrProtected)
}

func TestOpenRefusesCredentialStoreThatIsSymlink(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	require.NoError(t, os.RemoveAll(filepath.Join(home, ".ssh")))
	writeFile(t, filepath.Join(home, "dotfiles", "ssh", "id_x"), "private key")
	require.NoError(t, os.Symlink(filepath.Join("dotfiles", "ssh"), filepath.Join(home, ".ssh")))
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	for _, path := range []string{".ssh/id_x", "dotfiles/ssh/id_x"} {
		f, err := ws.Open(path)
		require.ErrorIs(t, err, ErrProtected, path)
		assert.Nil(t, f)
	}
}

func TestOpenRefusesCaseVariantOfProtectedStore(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	if _, err := os.Stat(filepath.Join(home, ".SSH")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	_, err := ws.Open(".SSH/id_x")
	require.ErrorIs(t, err, ErrProtected)
}

func TestOpenRefusesMissingPathsInProtectedStore(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	require.NoError(t, os.Symlink(".ssh", filepath.Join(home, "link")))
	require.NoError(t, os.Symlink("missing", filepath.Join(home, ".ssh", "dangling")))
	require.NoError(t, os.Symlink("../.env", filepath.Join(home, ".ssh", "outward")))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "x"), filepath.Join(home, ".ssh", "escape")))
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	for _, path := range []string{
		".ssh/nonexistent",
		".ssh/nodir/x",
		".ssh/id_x/x",
		"link/nonexistent",
		"kopi-home/nonexistent",
		".ssh/dangling",
		".ssh/outward",
		".ssh/escape",
	} {
		t.Run(path, func(t *testing.T) {
			f, err := ws.Open(path)
			require.ErrorIs(t, err, ErrProtected)
			assert.Nil(t, f)
			assert.NotErrorIs(t, err, fs.ErrNotExist)
		})
	}
}

func TestOpenRefusesFileStoreReplacedByRename(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))
	tmp := filepath.Join(home, ".claude.json.tmp")
	writeFile(t, tmp, `{"token":"new"}`)
	require.NoError(t, os.Rename(tmp, filepath.Join(home, ".claude.json")))

	_, err := ws.Open(".claude.json")
	require.ErrorIs(t, err, ErrProtected)
}

func TestOpenRefusesStoreCreatedAfterStart(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))
	writeFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "token")

	for _, path := range []string{".config/gh/hosts.yml", ".config/gh/missing"} {
		_, err := ws.Open(path)
		require.ErrorIs(t, err, ErrProtected, path)
	}
}

func TestOpenRefusesStoreRecreatedAfterStart(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))
	require.NoError(t, os.RemoveAll(filepath.Join(home, ".aws")))
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "aws secret")

	_, err := ws.Open(".aws/credentials")
	require.ErrorIs(t, err, ErrProtected)
}

func TestOpenSkipsProtectedPathUnderRegularFile(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	writeFile(t, filepath.Join(home, ".config"), "not a directory")
	writeFile(t, filepath.Join(home, "notes.txt"), "hello")
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	assert.Equal(t, "hello", readAll(t, ws, "notes.txt"))
	_, err := ws.Open(".ssh/id_x")
	require.ErrorIs(t, err, ErrProtected)
}

func TestNewWorkspaceRejectsRelativeProtectedPath(t *testing.T) {
	_, err := NewWorkspace(t.TempDir(), []string{".ssh"})
	require.Error(t, err)
}

func TestOpenReadsDotEnvInWorkspace(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	assert.Equal(t, "TOKEN=1", readAll(t, ws, ".env"))
}

func TestOpenReportsMissingFile(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	require.NoError(t, os.Mkdir(filepath.Join(home, "sub"), 0o755))
	ws := newWorkspace(t, home, DefaultProtected(home, kopiHome))

	for _, path := range []string{"missing.txt", "sub/missing.txt", "nodir/missing.txt"} {
		_, err := ws.Open(path)
		require.ErrorIs(t, err, fs.ErrNotExist, path)
		assert.Contains(t, err.Error(), path)
	}
}

func TestNewWorkspaceSkipsMissingProtectedEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	ws := newWorkspace(t, dir, DefaultProtected(filepath.Join(dir, "no-home"), filepath.Join(dir, "no-kopi")))

	assert.Equal(t, "hello", readAll(t, ws, "a.txt"))
}

func TestOpenRefusesEverythingInWorkspaceInsideProtectedStore(t *testing.T) {
	home, kopiHome := syntheticHome(t)
	dir := filepath.Join(home, ".ssh", "project")
	writeFile(t, filepath.Join(dir, "readme.txt"), "hello")
	ws := newWorkspace(t, dir, DefaultProtected(home, kopiHome))

	for _, path := range []string{"readme.txt", "."} {
		_, err := ws.Open(path)
		require.ErrorIs(t, err, ErrProtected, path)
	}
}
