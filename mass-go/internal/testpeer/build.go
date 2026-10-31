// Package testpeer builds the pinned SDK's strict offline protocol peer.
package testpeer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SDKVersion = "v0.0.0-20261031154401-ed6c8216607c"

// Build binds a provider to the SDK's current mock command. The scenario is an
// explicit test-launch environment input, never a default or a real-agent fallback.
func Build(ctx context.Context, dir, provider string) (string, error) {
	if provider != "codex" && provider != "claude" {
		return "", fmt.Errorf("unsupported test provider %q", provider)
	}
	tool := filepath.Join(dir, "agent-sdk-testify-"+provider)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tool, "github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build pinned protocol peer: %w\n%s", err, out)
	}
	quote := "'" + strings.ReplaceAll(tool, "'", "'\"'\"'") + "'"
	path := filepath.Join(dir, "mock-"+provider)
	script := "#!/bin/sh\nexec " + quote + " protocol mock --provider " + provider + " --scenario \"${" + strings.ToUpper(provider) + "_MOCK_SCENARIO:?explicit synthetic scenario required}\" -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", err
	}
	return path, nil
}
