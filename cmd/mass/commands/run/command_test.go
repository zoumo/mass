package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRejectsInvalidPermissions verifies that passing --permissions with an
// unknown value to the agent-run run() function returns a clear error and does NOT
// silently fall back to approve_all behavior.
func TestRunRejectsInvalidPermissions(t *testing.T) {
	// Create a minimal bundle directory with config.json.
	bundleDir := t.TempDir()
	configJSON := `{
		"massVersion": "0.1.0",
		"metadata": {"name": "test-agent"},
		"agentRoot": {"path": "workspace"},
		"acpAgent": {"process": {"command": "/bin/echo"}},
		"permissions": "approve_all"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "config.json"), []byte(configJSON), 0o600))

	// Simulate the cobra command with Flag("permissions").Changed = true
	// by calling NewCommand(). We use a cobra invocation to exercise the
	// flag-changed branch.
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"--bundle", bundleDir,
		"--permissions", "bad-value",
	})

	err := cmd.Execute()
	require.Error(t, err, "--permissions bad-value must return an error")
	assert.Contains(t, err.Error(), "invalid --permissions value",
		"error must explain that the permissions value is invalid")
}
