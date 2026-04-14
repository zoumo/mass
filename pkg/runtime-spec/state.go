package runtimespec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
)

const (
	stateFile      = "state.json"
	eventLogFile   = "events.jsonl"
	shimSocketFile = "agent-shim.sock"
)

// StateDir returns the directory used to persist state for the agent identified
// by id: baseDir/id/.
func StateDir(baseDir, id string) string {
	return filepath.Join(baseDir, id)
}

// EventLogPath returns the path to the JSONL event log for the given state dir.
func EventLogPath(stateDir string) string {
	return filepath.Join(stateDir, eventLogFile)
}

// ShimSocketPath returns the Unix socket path for the agent-shim RPC server.
// The socket lives inside the state dir so agentd can discover all running
// shims by scanning /run/agentd/shim/*/agent-shim.sock after a restart.
func ShimSocketPath(stateDir string) string {
	return filepath.Join(stateDir, shimSocketFile)
}

// ValidateShimSocketPath checks that the socket path does not exceed the OS
// limit for Unix domain socket paths (104 bytes on macOS, 108 on Linux).
func ValidateShimSocketPath(socketPath string) error {
	if len(socketPath) > maxUnixSocketPath {
		return fmt.Errorf("spec: socket path too long (%d bytes, max %d): %s — shorten the bundle root or agent name",
			len(socketPath), maxUnixSocketPath, socketPath)
	}
	return nil
}

// WriteState atomically writes s to dir/state.json.
// Atomicity is achieved by writing to a temp file then renaming it, which
// prevents partial reads if the process crashes mid-write.
func WriteState(dir string, s apiruntime.State) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("spec: mkdir %s: %w", dir, err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("spec: marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state.*.tmp")
	if err != nil {
		return fmt.Errorf("spec: create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up temp file on any error path.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("spec: write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("spec: close temp state file: %w", err)
	}
	target := filepath.Join(dir, stateFile)
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("spec: rename state file: %w", err)
	}
	committed = true
	return nil
}

// ReadState reads and unmarshals state.json from dir.
func ReadState(dir string) (apiruntime.State, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return apiruntime.State{}, fmt.Errorf("spec: read state.json: %w", err)
	}
	var s apiruntime.State
	if err := json.Unmarshal(data, &s); err != nil {
		return apiruntime.State{}, fmt.Errorf("spec: parse state.json: %w", err)
	}
	return s, nil
}

// DeleteState removes the entire state directory dir.
func DeleteState(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("spec: delete state dir %s: %w", dir, err)
	}
	return nil
}
