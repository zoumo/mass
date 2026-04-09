// Package workspace implements workspace preparation handlers.
// This file defines the GitHandler implementation for cloning git repositories
// as part of OAR workspace preparation.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitHandler implements SourceHandler for SourceTypeGit.
// It shells out to the git CLI (via exec.CommandContext) to clone repositories
// with support for branches, tags, commit SHAs, and shallow clones.
type GitHandler struct{}

// NewGitHandler creates a new GitHandler.
func NewGitHandler() *GitHandler {
	return &GitHandler{}
}

// Prepare clones a git repository into targetDir.
// It handles three clone modes based on source.Git.Ref:
//   - No ref (default): git clone --single-branch URL targetDir
//   - Branch/tag ref:    git clone --branch ref --single-branch URL targetDir
//   - Commit SHA ref:    clone first, then git checkout SHA in targetDir
//
// Depth is supported via --depth N --single-branch flags.
//
// Error handling:
//   - git not found: wrapped error with "git not found" context
//   - clone failure: wrapped error with URL, ref, and exit code
//   - checkout failure: wrapped error with SHA, URL, and exit code
//   - context cancellation: returns ctx.Err()
func (h *GitHandler) Prepare(ctx context.Context, source Source, targetDir string) (string, error) {
	if source.Type != SourceTypeGit {
		return "", fmt.Errorf("workspace: GitHandler cannot handle source type %q", source.Type)
	}

	git := source.Git
	if git.URL == "" {
		return "", fmt.Errorf("workspace: git source URL is required")
	}

	// Check if git is available before attempting clone.
	if _, err := exec.LookPath("git"); err != nil {
		return "", &GitError{
			Phase:   "lookup",
			URL:     git.URL,
			Ref:     git.Ref,
			Message: "git executable not found in PATH",
			Err:     err,
		}
	}

	// Determine if ref is a commit SHA (40 hex chars) or a branch/tag.
	isSHA := isCommitSHA(git.Ref)

	// Build clone arguments.
	cloneArgs := buildCloneArgs(git.URL, targetDir, git.Ref, git.Depth, !isSHA)

	// Execute git clone.
	//nolint:gosec // git command and arguments are constructed from validated config
	cloneCmd := exec.CommandContext(ctx, "git", cloneArgs...)
	// Set working directory to the parent of targetDir so git can create targetDir inside it.
	// If targetDir is "/tmp/work/repo", we run from "/tmp/work" so git creates "repo" there.
	cloneCmd.Dir = filepath.Dir(targetDir)

	if err := cloneCmd.Run(); err != nil {
		// Check for context cancellation first.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		exitCode := getExitCode(err)
		return "", &GitError{
			Phase:    "clone",
			URL:      git.URL,
			Ref:      git.Ref,
			ExitCode: exitCode,
			Message:  fmt.Sprintf("git clone failed (exit %d)", exitCode),
			Err:      err,
		}
	}

	// If ref is a commit SHA, checkout after clone.
	if isSHA && git.Ref != "" {
		checkoutArgs := []string{"checkout", git.Ref}
		//nolint:gosec // git checkout command constructed from validated config
		checkoutCmd := exec.CommandContext(ctx, "git", checkoutArgs...)
		checkoutCmd.Dir = targetDir // Run inside the cloned repo

		if err := checkoutCmd.Run(); err != nil {
			// Check for context cancellation.
			if ctx.Err() != nil {
				return "", ctx.Err()
			}

			exitCode := getExitCode(err)
			return "", &GitError{
				Phase:    "checkout",
				URL:      git.URL,
				Ref:      git.Ref,
				ExitCode: exitCode,
				Message:  fmt.Sprintf("git checkout %s failed (exit %d)", git.Ref, exitCode),
				Err:      err,
			}
		}
	}

	return targetDir, nil
}

// GitError is a structured error for git operations.
// It contains context about the operation phase, URL, ref, and underlying error.
type GitError struct {
	Phase    string // "lookup", "clone", or "checkout"
	URL      string // Repository URL
	Ref      string // Git ref (branch, tag, or SHA)
	ExitCode int    // Process exit code (0 if not applicable)
	Message  string // Human-readable error summary
	Err      error  // Underlying error (exec.ExitError, exec.LookPathError, etc.)
}

// Error implements the error interface.
func (e *GitError) Error() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("workspace: git %s failed", e.Phase))
	if e.URL != "" {
		parts = append(parts, fmt.Sprintf("url=%s", e.URL))
	}
	if e.Ref != "" {
		parts = append(parts, fmt.Sprintf("ref=%s", e.Ref))
	}
	if e.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit=%d", e.ExitCode))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil {
		parts = append(parts, fmt.Sprintf("error: %v", e.Err))
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying error for errors.Is and errors.As.
func (e *GitError) Unwrap() error {
	return e.Err
}

// isCommitSHA checks if ref looks like a 40-character hex commit SHA.
// Returns false for empty ref or refs that look like branches/tags.
func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if !isHexChar(c) {
			return false
		}
	}
	return true
}

// isHexChar checks if c is a valid hex digit (0-9, a-f, A-F).
func isHexChar(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// buildCloneArgs constructs git clone arguments based on URL, target, ref, depth,
// and whether to use --branch flag (false for SHA refs).
func buildCloneArgs(url, target, ref string, depth int, useBranchFlag bool) []string {
	args := []string{"clone"}

	// Add --depth for shallow clones.
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth))
	}

	// Always use --single-branch to avoid fetching all branches.
	args = append(args, "--single-branch")

	// Add --branch for named refs (branches/tags), but not for SHAs.
	if useBranchFlag && ref != "" {
		args = append(args, "--branch", ref)
	}

	// URL and target directory.
	args = append(args, url, target)

	return args
}

// getExitCode extracts the exit code from an exec error.
// Returns 1 if the exit code cannot be determined.
func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	exitErr := &exec.ExitError{}
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() != 0 {
			return exitErr.ExitCode()
		}
	}
	return 1
}
