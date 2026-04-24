package version

var (
	// Version is the daemon binary version, injected via ldflags.
	Version = "dev"
	// GitCommit is the git commit hash, injected via ldflags.
	GitCommit = "unknown"
	// BuildTime is the build timestamp, injected via ldflags (optional).
	BuildTime = ""
)

// String returns a human-readable version string.
func String() string {
	if GitCommit != "unknown" && GitCommit != "" {
		return Version + " (" + GitCommit + ")"
	}
	return Version
}
