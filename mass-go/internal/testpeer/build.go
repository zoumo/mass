// Package testpeer builds MASS's test-only strict offline protocol peer.
package testpeer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SDKVersion = "v0.0.0-20261106083916-f52be4d0377f"

// TestifyVersion pins the consumer recording/replay tool, not a runtime dependency.
const TestifyVersion = "v0.0.0-20261105165039-e4799fe3484c"

// Build binds a provider to MASS's owned strict scenario peer. The scenario is
// an explicit test-launch environment input, never a default or a real-agent
// fallback.
func Build(ctx context.Context, dir, provider string) (string, error) {
	if provider != "codex" && provider != "claude" {
		return "", fmt.Errorf("unsupported test provider %q", provider)
	}
	tool := filepath.Join(dir, "mass-testpeer-"+provider)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tool, "github.com/zoumo/mass/mass-go/internal/testpeer/cmd/testpeer")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build MASS protocol peer: %w\n%s", err, out)
	}
	quote := "'" + strings.ReplaceAll(tool, "'", "'\"'\"'") + "'"
	path := filepath.Join(dir, "mock-"+provider)
	script := "#!/bin/sh\nexec " + quote + " --provider " + provider + " --scenario \"${" + strings.ToUpper(provider) + "_MOCK_SCENARIO:?explicit synthetic scenario required}\" -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", err
	}
	return path, nil
}
