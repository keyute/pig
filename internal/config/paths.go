package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct{ Home, ConfigFile, AgentDir string }

// ResolvePaths locates Kopi's home without creating anything: $KOPI_HOME
// (made absolute) when set and non-empty, else <user home>/.kopi.
func ResolvePaths() (Paths, error) {
	home := os.Getenv("KOPI_HOME")
	if home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Paths{}, fmt.Errorf("failed to resolve KOPI_HOME %q: %w", home, err)
		}
		home = abs
	} else {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("%w: %w", ErrNoHome, err)
		}
		home = filepath.Join(userHome, ".kopi")
	}
	return Paths{
		Home:       home,
		ConfigFile: filepath.Join(home, "config.json"),
		AgentDir:   filepath.Join(home, "agent"),
	}, nil
}
