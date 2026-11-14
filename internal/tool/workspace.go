package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// rootEscapeMessage is the text of os.Root's unexported errPathEscapes. Go
// exports no sentinel for it (os.ErrPathEscapes exists only in os's own
// tests), so the inner error of the *fs.PathError is compared by text.
const rootEscapeMessage = "path escapes from parent"

// maxSymlinks bounds symlink expansion in resolve; os.Root gives up after the
// same number (rootMaxSymlinks in go1.27).
const maxSymlinks = 8

type Workspace struct {
	root *os.Root
	// given is the absolute workspace path as passed to NewWorkspace; realDir
	// is its symlink-resolved form. Absolute paths may be expressed against
	// either.
	given, realDir string
	// protected holds paths, not identities: stores are rewritten by
	// rename-over (new inode) and may appear after startup, so they are
	// stat'd afresh on every Open.
	protected []string
}

func NewWorkspace(dir string, protected []string) (*Workspace, error) {
	given, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %s: %w", dir, err)
	}
	realDir, err := filepath.EvalSymlinks(given)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %s: %w", dir, err)
	}
	for _, p := range protected {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("protected path %q is not absolute", p)
		}
	}
	root, err := os.OpenRoot(realDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open workspace %s: %w", dir, err)
	}
	return &Workspace{root: root, given: given, realDir: realDir, protected: protected}, nil
}

func DefaultProtected(home, kopiHome string) []string {
	paths := []string{kopiHome}
	for _, name := range []string{
		".ssh", ".aws", ".gnupg", ".config/gcloud", ".config/gh", ".netrc",
		".git-credentials", ".npmrc", ".docker", ".kube", ".azure",
		".pi", ".pig", ".claude", ".claude.json", ".codex",
	} {
		paths = append(paths, filepath.Join(home, filepath.FromSlash(name)))
	}
	return paths
}

func (w *Workspace) Open(path string) (*os.File, error) {
	rel, err := w.relative(path)
	if err != nil {
		return nil, err
	}
	// O_NONBLOCK stops a FIFO open from waiting for a writer, which nothing
	// could interrupt; it has no effect on regular files and directories.
	f, err := w.root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// The protected check runs before the failure is reported, so a path
		// into a protected store is refused alike whether or not it exists.
		_, protected, rerr := w.resolve(rel)
		if rerr != nil {
			return nil, rerr
		}
		if protected {
			return nil, fmt.Errorf("%w: %s", ErrProtected, path)
		}
		if pe, ok := errors.AsType[*fs.PathError](err); ok {
			if pe.Err.Error() == rootEscapeMessage {
				return nil, fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
			}
			err = pe.Err
		}
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	if err := w.check(f, rel, path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (w *Workspace) Close() error { return w.root.Close() }

func (w *Workspace) relative(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return path, nil
	}
	for _, base := range []string{w.given, w.realDir} {
		if rel, err := filepath.Rel(base, path); err == nil && filepath.IsLocal(rel) {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
}

// check runs after f was opened through os.Root. It refuses f if resolving
// rel enters a protected entry, if f is not the file rel resolves to now, or
// if it is neither a regular file nor a directory.
func (w *Workspace) check(f *os.File, rel, path string) error {
	target, protected, err := w.resolve(rel)
	if err != nil {
		return err
	}
	if protected {
		return fmt.Errorf("%w: %s", ErrProtected, path)
	}
	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", path, err)
	}
	resolved, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", path, err)
	}
	// rel was resolved after os.Root opened it, so a rename or symlink swap in
	// between could make the protected check judge a different file than the
	// one handed back; requiring the same identity binds the check to f.
	if !os.SameFile(opened, resolved) {
		return fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
	}
	if !opened.Mode().IsRegular() && !opened.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return nil
}

// resolve follows rel from the workspace one component at a time, expanding
// symlinks and ".." as os.Root does, and returns the real path reached and
// whether any step lies in a protected entry. It stops at the first component
// that cannot be resolved, so a missing path ends at its deepest existing
// ancestor. Every step is checked, not only the end, so a path that fails or
// leaves again inside a store (a dangling or outward symlink there) is refused
// too, and a store's contents cannot be probed through error kinds.
//
// The workspace may be the user's home, so stores can lie inside it. Identity
// is compared with os.SameFile rather than path prefixes because APFS is
// case-insensitive (~/.SSH is ~/.ssh) and symlinks can name a store under
// another spelling.
func (w *Workspace) resolve(rel string) (string, bool, error) {
	stores, err := w.statProtected()
	if err != nil {
		return "", false, err
	}
	isProtected := func(info os.FileInfo) bool {
		return slices.ContainsFunc(stores, func(s os.FileInfo) bool { return os.SameFile(info, s) })
	}
	// The workspace may itself lie inside a store. Every ancestor of a later
	// step is one of these or an earlier step, so steps need no ancestor walk.
	for p := w.realDir; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return "", false, fmt.Errorf("failed to stat %s: %w", p, err)
		}
		if isProtected(info) {
			return p, true, nil
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	sep := string(filepath.Separator)
	dest := w.realDir
	parts := strings.Split(rel, sep)
	for links := 0; len(parts) > 0; {
		name := parts[0]
		parts = parts[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			dest = filepath.Dir(dest)
			continue
		}
		next := filepath.Join(dest, name)
		info, err := os.Lstat(next)
		if err != nil {
			return dest, false, nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			link, err := os.Readlink(next)
			if err != nil || links > maxSymlinks {
				return dest, false, nil
			}
			if filepath.IsAbs(link) {
				dest = sep
			}
			parts = append(strings.Split(link, sep), parts...)
			continue
		}
		dest = next
		if isProtected(info) {
			return dest, true, nil
		}
	}
	return dest, false, nil
}

// statProtected returns the current identities of the protected paths that
// exist. Any other stat failure is returned so the caller fails closed rather
// than treating an unknown store as absent.
func (w *Workspace) statProtected() ([]os.FileInfo, error) {
	var stores []os.FileInfo
	for _, p := range w.protected {
		info, err := os.Stat(p)
		// ENOTDIR means a parent is not a directory, so the path cannot name a
		// store any more than a missing one can.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to check protected path: %w", err)
		}
		stores = append(stores, info)
	}
	return stores, nil
}
