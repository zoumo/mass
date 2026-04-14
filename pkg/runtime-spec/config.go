package runtimespec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
)

// ParseConfig reads and unmarshals config.json from bundlePath.
// bundlePath is the agent bundle directory (config.json lives at bundlePath/config.json).
func ParseConfig(bundlePath string) (apiruntime.Config, error) {
	cfgPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return apiruntime.Config{}, fmt.Errorf("spec: read config.json: %w", err)
	}
	var c apiruntime.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return apiruntime.Config{}, fmt.Errorf("spec: parse config.json: %w", err)
	}
	return c, nil
}

// ValidateConfig checks that c satisfies the OAR Runtime Specification
// validation rules:
//   - oarVersion must be non-empty and have major version == 0
//   - metadata.name must be non-empty
//   - agentRoot.path must be non-empty and must not be an absolute path
//   - acpAgent.process.command must be non-empty
//   - permissions must be a known PermissionPolicy value (or empty, which
//     defaults to ApproveAll)
func ValidateConfig(c apiruntime.Config) error {
	if c.OarVersion == "" {
		return fmt.Errorf("spec: oarVersion is required")
	}
	major, err := parseMajor(c.OarVersion)
	if err != nil {
		return fmt.Errorf("spec: oarVersion %q is not a valid SemVer: %w", c.OarVersion, err)
	}
	if major != 0 {
		return fmt.Errorf("spec: unsupported oarVersion major %d (only 0.x.x is supported)", major)
	}
	if c.Metadata.Name == "" {
		return fmt.Errorf("spec: metadata.name is required")
	}
	if c.AgentRoot.Path == "" {
		return fmt.Errorf("spec: agentRoot.path is required")
	}
	if filepath.IsAbs(c.AgentRoot.Path) {
		return fmt.Errorf("spec: agentRoot.path must be a relative path, got %q", c.AgentRoot.Path)
	}
	if c.AcpAgent.Process.Command == "" {
		return fmt.Errorf("spec: acpAgent.process.command is required")
	}
	// Empty permissions defaults to ApproveAll — treat as valid.
	if c.Permissions != "" && !c.Permissions.IsValid() {
		return fmt.Errorf("spec: unknown permissions value %q (valid: approve_all, approve_reads, deny_all)", c.Permissions)
	}
	return nil
}

// ResolveAgentRoot resolves agentRoot.path to a canonical absolute path.
// Steps:
//  1. filepath.Abs(bundleDir) — ensure bundleDir is absolute regardless of cwd
//  2. filepath.Join with agentRoot.path — produce the candidate path
//  3. filepath.EvalSymlinks — follow any symlink, returning the real path
//
// The result is used as cmd.Dir and as the ACP session/new cwd parameter.
func ResolveAgentRoot(bundleDir string, cfg apiruntime.Config) (string, error) {
	absBundleDir, err := filepath.Abs(bundleDir)
	if err != nil {
		return "", fmt.Errorf("spec: abs bundleDir %q: %w", bundleDir, err)
	}
	joined := filepath.Join(absBundleDir, cfg.AgentRoot.Path)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("spec: resolve agentRoot %q in bundle %q: %w", cfg.AgentRoot.Path, bundleDir, err)
	}
	return resolved, nil
}

// parseMajor extracts the major version integer from a SemVer string.
// Accepts "MAJOR.MINOR.PATCH" and "MAJOR.MINOR.PATCH-pre+build" forms.
func parseMajor(version string) (int, error) {
	// Strip any build metadata or pre-release suffix after the first '-' or '+'.
	core := version
	if i := strings.IndexAny(core, "-+"); i != -1 {
		core = core[:i]
	}
	parts := strings.SplitN(core, ".", 3)
	if len(parts) < 1 || parts[0] == "" {
		return 0, fmt.Errorf("empty major component")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("non-numeric major %q: %w", parts[0], err)
	}
	return major, nil
}
