package pigruntime

import "path/filepath"

// IsolationEnv is the process environment PiG must see. PiG resolves its
// default agent dir even when one is passed explicitly (session lookup) and
// panics when no home is resolvable, so both roots are pinned under Kopi home
// rather than left to fall back to ~/.pig or ~/.pi.
func IsolationEnv(kopiHome, agentDir string) (set map[string]string, unset []string) {
	set = map[string]string{
		"PIG_CODING_AGENT_DIR": agentDir,
		"PIG_HOME":             filepath.Join(kopiHome, "pig"),
	}
	unset = []string{"PIG_USE_PI_DIRS", "PIG_TEST_FAUX"}
	return set, unset
}
