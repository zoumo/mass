// Package runtime implements terminal operations for agent workspace command execution.
// TerminalManager handles the lifecycle of terminal processes: create, output, kill, release, wait.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// DefaultOutputByteLimit is the default maximum output bytes to retain (1MB).
const DefaultOutputByteLimit = 1048576

// TerminalState represents the current state of a terminal process.
type TerminalState string

const (
	TerminalStateRunning TerminalState = "running"
	TerminalStateExited  TerminalState = "exited"
	TerminalStateKilled  TerminalState = "killed"
)

// Terminal tracks a single terminal process and its output.
type Terminal struct {
	ID             string
	State          TerminalState
	Cmd            *exec.Cmd
	Output         *bytes.Buffer
	OutputLimit    int
	Truncated      bool
	ExitCode       *int
	Signal         *string
	done           chan struct{}
	mu             sync.Mutex
}

// TerminalManager manages multiple terminal processes for a session.
type TerminalManager struct {
	terminals map[string]*Terminal
	mu        sync.RWMutex
	workDir   string // resolved agent root directory
	env       []string // merged environment
	policy    spec.PermissionPolicy
}

// NewTerminalManager creates a new TerminalManager.
func NewTerminalManager(workDir string, env []string, policy spec.PermissionPolicy) *TerminalManager {
	return &TerminalManager{
		terminals: make(map[string]*Terminal),
		workDir:   workDir,
		env:       env,
		policy:    policy,
	}
}

// checkPermission returns an error if terminal operations are blocked by policy.
// Terminal operations are allowed only for approve-all policy.
// approve-reads and deny-all both block terminal operations.
func (m *TerminalManager) checkPermission() error {
	switch m.policy {
	case spec.ApproveAll, "":
		return nil // allowed
	case spec.ApproveReads:
		return fmt.Errorf("permission denied: approve-reads policy blocks terminal operations")
	case spec.DenyAll:
		return fmt.Errorf("permission denied: deny-all policy blocks terminal operations")
	default:
		return fmt.Errorf("permission denied: unknown policy %s blocks terminal operations", m.policy)
	}
}

// Create executes a new terminal command and returns its terminal ID.
// The command runs in the agent's workspace directory with merged environment.
// Output is captured in a buffer with optional byte limit truncation.
func (m *TerminalManager) Create(ctx context.Context, command string, args []string, cwd *string, env []string, outputByteLimit *int) (string, error) {
	// Check permission policy first
	if err := m.checkPermission(); err != nil {
		return "", err
	}

	// Generate unique terminal ID
	terminalID := uuid.New().String()

	// Determine output byte limit (default 1MB if not specified)
	limit := DefaultOutputByteLimit
	if outputByteLimit != nil && *outputByteLimit > 0 {
		limit = *outputByteLimit
	}

	// Build the command
	//nolint:gosec // command comes from agent request
	cmd := exec.CommandContext(ctx, command, args...)

	// Set working directory
	// If cwd is specified, use it; otherwise use the agent's workspace root
	if cwd != nil && *cwd != "" {
		cmd.Dir = *cwd
	} else {
		cmd.Dir = m.workDir
	}

	// Merge environment: start with manager's base env, overlay request env
	cmd.Env = mergeEnv(m.env, env)

	// Create output buffer with limit
	output := bytes.NewBuffer(make([]byte, 0, min(limit, 4096)))

	// Capture stdout and stderr into the same buffer
	// Use a MultiWriter to combine both streams
	var outputWriter io.Writer = output

	// Create a limited writer that truncates from beginning when limit exceeded
	limitedWriter := &truncatingWriter{
		buffer:  output,
		limit:   limit,
		truncated: false,
	}
	outputWriter = limitedWriter

	// Pipe stdout and stderr to the output writer
	cmd.Stdout = outputWriter
	cmd.Stderr = outputWriter

	// Start the process
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("terminal: start command: %w", err)
	}

	// Create terminal tracking struct
	terminal := &Terminal{
		ID:          terminalID,
		State:       TerminalStateRunning,
		Cmd:         cmd,
		Output:      output,
		OutputLimit: limit,
		Truncated:   false,
		done:        make(chan struct{}),
	}

	// Register terminal
	m.mu.Lock()
	m.terminals[terminalID] = terminal
	m.mu.Unlock()

	// Start background goroutine to wait for process exit
	go m.waitForExit(terminal, limitedWriter)

	return terminalID, nil
}

// waitForExit waits for the terminal process to complete and updates its state.
func (m *TerminalManager) waitForExit(terminal *Terminal, tw *truncatingWriter) {
	defer close(terminal.done)

	err := terminal.Cmd.Wait()

	terminal.mu.Lock()
	defer terminal.mu.Unlock()

	// Update truncation flag from writer
	terminal.Truncated = tw.truncated

	if err != nil {
		// Process exited with error - extract exit code/signal
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
				if status.Exited() {
					exitCode := status.ExitStatus()
					terminal.ExitCode = &exitCode
				} else if status.Signaled() {
					sig := status.Signal().String()
					terminal.Signal = &sig
				}
			}
		}
	} else {
		// Process exited successfully (exit code 0)
		exitCode := 0
		terminal.ExitCode = &exitCode
	}

	// Update state based on whether it was killed or exited normally
	if terminal.Signal != nil {
		terminal.State = TerminalStateKilled
	} else {
		terminal.State = TerminalStateExited
	}
}

// Output returns the captured output and exit status for a terminal.
func (m *TerminalManager) Output(terminalID string) (string, bool, *int, *string, error) {
	m.mu.RLock()
	terminal, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return "", false, nil, nil, fmt.Errorf("terminal: not found: %s", terminalID)
	}

	terminal.mu.Lock()
	defer terminal.mu.Unlock()

	output := terminal.Output.String()
	truncated := terminal.Truncated

	return output, truncated, terminal.ExitCode, terminal.Signal, nil
}

// Kill sends SIGTERM to the terminal process, then SIGKILL after timeout.
func (m *TerminalManager) Kill(terminalID string) error {
	m.mu.RLock()
	terminal, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("terminal: not found: %s", terminalID)
	}

	terminal.mu.Lock()
	state := terminal.State
	cmd := terminal.Cmd
	terminal.mu.Unlock()

	if state != TerminalStateRunning {
		// Already exited or killed - nothing to do
		return nil
	}

	// Send SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process may have already exited; try SIGKILL
		_ = cmd.Process.Kill()
		return nil
	}

	// Wait up to 5 seconds for graceful exit
	select {
	case <-terminal.done:
		return nil // Process exited gracefully
	case <-time.After(5 * time.Second):
		// Force kill
		_ = cmd.Process.Kill()
		// Wait for process to be reaped
		select {
		case <-terminal.done:
		case <-time.After(2 * time.Second):
			// Process should be dead now
		}
		return nil
	}
}

// Release removes a terminal from the manager and cleans up resources.
// If the process is still running, it will be killed first.
func (m *TerminalManager) Release(terminalID string) error {
	m.mu.Lock()
	terminal, ok := m.terminals[terminalID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("terminal: not found: %s", terminalID)
	}
	delete(m.terminals, terminalID)
	m.mu.Unlock()

	// If still running, kill it
	terminal.mu.Lock()
	state := terminal.State
	cmd := terminal.Cmd
	terminal.mu.Unlock()

	if state == TerminalStateRunning && cmd.Process != nil {
		_ = cmd.Process.Kill()
		// Wait for process to be reaped
		select {
		case <-terminal.done:
		case <-time.After(1 * time.Second):
		}
	}

	// Clean up output buffer
	terminal.mu.Lock()
	terminal.Output.Reset()
	terminal.mu.Unlock()

	return nil
}

// WaitForExit blocks until the terminal process exits and returns exit status.
func (m *TerminalManager) WaitForExit(ctx context.Context, terminalID string) (*int, *string, error) {
	m.mu.RLock()
	terminal, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("terminal: not found: %s", terminalID)
	}

	// Wait for process exit or context cancellation
	select {
	case <-terminal.done:
		// Process exited - return exit status
		terminal.mu.Lock()
		exitCode := terminal.ExitCode
		signal := terminal.Signal
		terminal.mu.Unlock()
		return exitCode, signal, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// truncatingWriter wraps a bytes.Buffer and truncates from the beginning
// when the output exceeds the byte limit, ensuring UTF-8 character boundaries.
type truncatingWriter struct {
	buffer    *bytes.Buffer
	limit     int
	truncated bool
	mu        sync.Mutex
}

// Write writes data to the buffer, truncating from the beginning if limit exceeded.
func (tw *truncatingWriter) Write(p []byte) (n int, err error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	// Write the new data
	n, err = tw.buffer.Write(p)
	if err != nil {
		return n, err
	}

	// Check if we need to truncate
	if tw.buffer.Len() > tw.limit {
		// Calculate how much to truncate (keep the most recent bytes)
		excess := tw.buffer.Len() - tw.limit

		// Get current buffer contents
		data := tw.buffer.Bytes()

		// Find a valid UTF-8 boundary for truncation
		// Start from the excess position and find the next valid UTF-8 start
		truncatePos := excess
		for truncatePos < len(data) {
			// Check if this position is a valid UTF-8 start byte
			// A valid start byte is either:
			// - 0xxxxxxx (ASCII, single byte)
			// - 110xxxxx (2-byte sequence start)
			// - 1110xxxx (3-byte sequence start)
			// - 11110xxx (4-byte sequence start)
			b := data[truncatePos]
			if b < 0x80 || (b >= 0xC0 && b < 0xC2) || b >= 0xF8 {
				// Valid start byte found (or continuation byte 0x80-0xBF are not starts)
				// Actually continuation bytes are 10xxxxxx (0x80-0xBF)
				// We need to skip continuation bytes (10xxxxxx pattern)
				break
			}
			// Skip this continuation byte and try next position
			truncatePos++
		}

		// Truncate from beginning up to truncatePos
		// Keep data from truncatePos onwards
		newData := data[truncatePos:]
		tw.buffer.Reset()
		tw.buffer.Write(newData)
		tw.truncated = true
	}

	return n, nil
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}