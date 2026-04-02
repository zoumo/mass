// Package workspace defines the OAR Workspace Specification types and handlers.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitHandler implements SourceHandler for git repository sources.
// It shells out to the git CLI to clone repositories with ref/depth support.
type GitHandler struct {
	// gitPath is the path to the git executable.
	// If empty, defaults to "git" (resolved via exec.LookPath on first use).
	gitPath string
}

// NewGitHandler creates a new GitHandler.
// It resolves the git executable path and returns an error if git is not found.
func NewGitHandler() (*GitHandler, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("workspace: git not found: %w", err)
	}
	return &GitHandler{gitPath: gitPath}, nil
}

// Prepare clones a git repository to targetDir.
//
// Clone modes:
//   - Default (no ref): git clone URL targetDir
//     If depth > 0, adds --depth N --single-branch for shallow clone
//   - Ref (branch/tag): git clone --branch ref --single-branch URL targetDir
//     If depth > 0, also adds --depth N
//   - SHA: clone first (with depth if specified), then git checkout SHA
//
// Returns targetDir (the cloned repository path).
// Returns structured errors with phase (clone/checkout), URL, ref context.
func (h *GitHandler) Prepare(ctx context.Context, source Source, targetDir string) (string, error) {
	// Validate source type.
	if source.Type != SourceTypeGit {
		return "", fmt.Errorf("workspace: GitHandler received source type %q (expected git)", source.Type)
	}

	// Validate git URL.
	if source.Git.URL == "" {
		return "", fmt.Errorf("workspace: git source URL is required")
	}

	// Ensure target directory parent exists.
	if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
		return "", fmt.Errorf("workspace: create target parent directory: %w", err)
	}

	// Determine clone mode based on ref.
	ref := source.Git.Ref
	depth := source.Git.Depth

	if ref == "" {
		// Default clone (no ref).
		if err := h.cloneDefault(ctx, source.Git.URL, targetDir, depth); err != nil {
			return "", err
		}
	} else if isLikelySHA(ref) {
		// SHA clone: clone first, then checkout.
		if err := h.cloneSHA(ctx, source.Git.URL, targetDir, ref, depth); err != nil {
			return "", err
		}
	} else {
		// Ref clone (branch/tag).
		if err := h.cloneRef(ctx, source.Git.URL, targetDir, ref, depth); err != nil {
			return "", err
		}
	}

	return targetDir, nil
}

// cloneDefault clones without a specific ref.
// If depth > 0, uses --depth N --single-branch for shallow clone.
func (h *GitHandler) cloneDefault(ctx context.Context, url, targetDir string, depth int) error {
	args := []string{"clone"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth), "--single-branch")
	}
	args = append(args, url, targetDir)

	if err := h.runGit(ctx, args, "", "clone", url, ""); err != nil {
		return err
	}
	return nil
}

// cloneRef clones a specific branch/tag.
// Uses --branch ref --single-branch. If depth > 0, adds --depth N.
func (h *GitHandler) cloneRef(ctx context.Context, url, targetDir string, ref string, depth int) error {
	args := []string{"clone", "--branch", ref, "--single-branch"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth))
	}
	args = append(args, url, targetDir)

	if err := h.runGit(ctx, args, "", "clone", url, ref); err != nil {
		return err
	}
	return nil
}

// cloneSHA clones first then checks out a specific SHA.
// If depth > 0, clones shallowly but without --single-branch (may need history).
func (h *GitHandler) cloneSHA(ctx context.Context, url, targetDir string, sha string, depth int) error {
	// Clone without --single-branch (SHA may not be on default branch).
	args := []string{"clone"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth))
		// Note: For SHA checkout with shallow clone, if SHA is not within depth,
		// checkout will fail. This is expected behavior.
	}
	args = append(args, url, targetDir)

	if err := h.runGit(ctx, args, "", "clone", url, ""); err != nil {
		return err
	}

	// Checkout the SHA in the cloned directory.
	checkoutArgs := []string{"checkout", sha}
	if err := h.runGit(ctx, checkoutArgs, targetDir, "checkout", url, sha); err != nil {
		return err
	}

	return nil
}

// runGit executes a git command with context and structured error handling.
// workingDir is the directory to run the command in (empty for clone, targetDir for checkout).
// phase is the operation phase for error context ("clone" or "checkout").
// url and ref are included in error messages for debugging.
func (h *GitHandler) runGit(ctx context.Context, args []string, workingDir, phase, url, ref string) error {
	//nolint:gosec // gitPath is resolved from exec.LookPath, args are controlled
	cmd := exec.CommandContext(ctx, h.gitPath, args...)
	cmd.Dir = workingDir
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}

	// Handle context cancellation.
	if ctx.Err() != nil {
		return fmt.Errorf("workspace: git %s cancelled (url=%q, ref=%q): %w", phase, url, ref, ctx.Err())
	}

	// Handle exec error (git not executable, etc.).
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q): %w", phase, url, ref, execErr)
	}

	// Handle exit error (git returned non-zero exit code).
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q, exit=%d): %w",
			phase, url, ref, exitErr.ExitCode(), exitErr)
	}

	// Generic error wrap.
	return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q): %w", phase, url, ref, err)
}

// isLikelySHA checks if ref looks like a git commit SHA.
// Git SHAs are hexadecimal strings of 7-40 characters.
// Short SHAs (7+ chars) are common; full SHAs are 40 chars.
func isLikelySHA(ref string) bool {
	// SHA must be 7-40 hex characters.
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	// All characters must be hex digits.
	for _, c := range ref {
		if !isHexDigit(c) {
			return false
		}
	}
	return true
}

// isHexDigit checks if c is a hexadecimal digit (0-9, a-f, A-F).
func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// GitHandlerWithMockExec is a variant of GitHandler for testing.
// It uses a mock exec function instead of shelling out to git.
type GitHandlerWithMockExec struct {
	execFunc func(ctx context.Context, name string, args []string, dir string) error
}

// NewGitHandlerWithMockExec creates a GitHandler with a mock exec function.
// The mock is used for testing without shelling out to git.
func NewGitHandlerWithMockExec(execFunc func(ctx context.Context, name string, args []string, dir string) error) *GitHandlerWithMockExec {
	return &GitHandlerWithMockExec{execFunc: execFunc}
}

// Prepare implements SourceHandler using the mock exec function.
func (h *GitHandlerWithMockExec) Prepare(ctx context.Context, source Source, targetDir string) (string, error) {
	// Validate source type.
	if source.Type != SourceTypeGit {
		return "", fmt.Errorf("workspace: GitHandler received source type %q (expected git)", source.Type)
	}

	// Validate git URL.
	if source.Git.URL == "" {
		return "", fmt.Errorf("workspace: git source URL is required")
	}

	// Ensure target directory parent exists.
	if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
		return "", fmt.Errorf("workspace: create target parent directory: %w", err)
	}

	// Determine clone mode based on ref.
	ref := source.Git.Ref
	depth := source.Git.Depth

	// Build git arguments based on mode.
	var cloneArgs []string
	var checkoutArgs []string
	var needsCheckout bool

	if ref == "" {
		// Default clone.
		cloneArgs = []string{"clone"}
		if depth > 0 {
			cloneArgs = append(cloneArgs, "--depth", fmt.Sprintf("%d", depth), "--single-branch")
		}
		cloneArgs = append(cloneArgs, source.Git.URL, targetDir)
	} else if isLikelySHA(ref) {
		// SHA clone.
		cloneArgs = []string{"clone"}
		if depth > 0 {
			cloneArgs = append(cloneArgs, "--depth", fmt.Sprintf("%d", depth))
		}
		cloneArgs = append(cloneArgs, source.Git.URL, targetDir)
		checkoutArgs = []string{"checkout", ref}
		needsCheckout = true
	} else {
		// Ref clone.
		cloneArgs = []string{"clone", "--branch", ref, "--single-branch"}
		if depth > 0 {
			cloneArgs = append(cloneArgs, "--depth", fmt.Sprintf("%d", depth))
		}
		cloneArgs = append(cloneArgs, source.Git.URL, targetDir)
	}

	// Run clone.
	if err := h.execFunc(ctx, "git", cloneArgs, ""); err != nil {
		return "", wrapGitError(ctx, err, "clone", source.Git.URL, ref)
	}

	// Run checkout if needed.
	if needsCheckout {
		if err := h.execFunc(ctx, "git", checkoutArgs, targetDir); err != nil {
			return "", wrapGitError(ctx, err, "checkout", source.Git.URL, ref)
		}
	}

	return targetDir, nil
}

// wrapGitError creates a structured error from exec errors.
func wrapGitError(ctx context.Context, err error, phase, url, ref string) error {
	// Handle context cancellation.
	if ctx.Err() != nil {
		return fmt.Errorf("workspace: git %s cancelled (url=%q, ref=%q): %w", phase, url, ref, ctx.Err())
	}

	// Handle exec error.
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q): %w", phase, url, ref, execErr)
	}

	// Handle exit error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q, exit=%d): %w",
			phase, url, ref, exitErr.ExitCode(), exitErr)
	}

	// Generic wrap.
	return fmt.Errorf("workspace: git %s failed (url=%q, ref=%q): %w", phase, url, ref, err)
}

// GitError represents a structured error from git operations.
// It contains phase, URL, ref, and underlying error for agent inspection.
type GitError struct {
	Phase   string // "clone" or "checkout"
	URL     string // repository URL
	Ref     string // ref or SHA (may be empty)
	ExitCode int   // exit code (0 if not an ExitError)
	Err     error  // underlying error
}

// Error implements error interface.
func (e *GitError) Error() string {
	if e.ExitCode > 0 {
		return fmt.Sprintf("workspace: git %s failed (url=%q, ref=%q, exit=%d): %s",
			e.Phase, e.URL, e.Ref, e.ExitCode, e.Err.Error())
	}
	return fmt.Sprintf("workspace: git %s failed (url=%q, ref=%q): %s",
		e.Phase, e.URL, e.Ref, e.Err.Error())
}

// Unwrap returns the underlying error.
func (e *GitError) Unwrap() error {
	return e.Err
}

// ParseGitError extracts structured information from a git error.
// Returns nil if the error is not a git-related error.
func ParseGitError(err error) *GitError {
	// Check if error message contains "workspace: git" prefix.
	if err == nil || !strings.Contains(err.Error(), "workspace: git") {
		return nil
	}

	// Parse the error message to extract phase, url, ref, exit code.
	// This is a simple heuristic for agent inspection.
	// Format: "workspace: git <phase> failed (url=<url>, ref=<ref>, exit=<code>): ..."
	msg := err.Error()
	var ge GitError
	ge.Err = err

	// Extract phase.
	if strings.Contains(msg, "git clone") {
		ge.Phase = "clone"
	} else if strings.Contains(msg, "git checkout") {
		ge.Phase = "checkout"
	}

	// Extract URL.
	if idx := strings.Index(msg, "url="); idx >= 0 {
		rest := msg[idx+4:]
		if end := strings.Index(rest, ","); end >= 0 {
			ge.URL = strings.Trim(rest[:end], "\"")
		}
	}

	// Extract ref.
	if idx := strings.Index(msg, "ref="); idx >= 0 {
		rest := msg[idx+4:]
		if end := strings.Index(rest, ","); end >= 0 {
			ge.Ref = strings.Trim(rest[:end], "\"")
		}
	}

	// Extract exit code.
	if idx := strings.Index(msg, "exit="); idx >= 0 {
		rest := msg[idx+5:]
		if end := strings.Index(rest, ":"); end >= 0 {
			ge.ExitCode = parseInt(rest[:end])
		}
	}

	return &ge
}

// parseInt parses a decimal integer from a string.
// Returns 0 if parsing fails.
func parseInt(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	return n
}