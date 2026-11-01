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

const SDKVersion = "v0.0.0-20261101172223-619cf8ac7e14"

// Build binds both providers to one SDK mock binary. The scenario is an
// explicit test-launch environment input, never a default or a real-agent fallback.
func Build(ctx context.Context, dir string) (map[string]string, error) {
	tool := filepath.Join(dir, "agent-sdk-testify")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tool, "github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build pinned protocol peer: %w\n%s", err, out)
	}
	quote := "'" + strings.ReplaceAll(tool, "'", "'\"'\"'") + "'"
	paths := make(map[string]string, 2)
	for _, provider := range []string{"codex", "claude"} {
		path := filepath.Join(dir, "mock-"+provider)
		script := "#!/bin/sh\nexec " + quote + " protocol mock --provider " + provider + " --scenario \"${" + strings.ToUpper(provider) + "_MOCK_SCENARIO:?explicit synthetic scenario required}\" -- \"$@\"\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			return nil, err
		}
		paths[provider] = path
	}
	return paths, nil
}
