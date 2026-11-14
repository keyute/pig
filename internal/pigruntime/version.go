package pigruntime

import "runtime/debug"

const modulePath = "github.com/MichaelKinsy/PiG"

// Version is the PiG module version linked into the running binary, or
// "unknown" without build info.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			return dep.Version
		}
	}
	return "unknown"
}
