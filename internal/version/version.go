package version

import "runtime"

var (
	// Version is the daemon binary version, injected via ldflags.
	Version = "dev"
	// GitCommit is the git commit hash, injected via ldflags.
	GitCommit = "unknown"
	// BuildTime is the build timestamp, injected via ldflags (optional).
	BuildTime = ""
	// GoVersion is the Go version used to build, injected via ldflags.
	GoVersion = runtime.Version()
)

// String returns a human-readable version string.
func String() string {
	if GitCommit != "unknown" && GitCommit != "" {
		return Version + " (" + GitCommit + ")"
	}
	return Version
}

// Full returns full version info including Go version.
func Full() string {
	return String() + ", go" + GoVersion
}
