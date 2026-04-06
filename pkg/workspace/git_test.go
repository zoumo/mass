// Package workspace implements workspace preparation handlers.
// This file tests the GitHandler implementation.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGitHandlerRejectsNonGitSource verifies GitHandler returns error for wrong source type.
func TestGitHandlerRejectsNonGitSource(t *testing.T) {
	h := NewGitHandler()

	tests := []struct {
		name   string
		source Source
	}{
		{
			name: "emptyDir source",
			source: Source{
				Type:     SourceTypeEmptyDir,
				EmptyDir: EmptyDirSource{},
			},
		},
		{
			name: "local source",
			source: Source{
				Type:  SourceTypeLocal,
				Local: LocalSource{Path: "/tmp/some/path"},
			},
		},
		{
			name: "unknown source type",
			source: Source{
				Type: SourceType("unknown"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.Prepare(context.Background(), tt.source, "/tmp/target")
			if err == nil {
				t.Fatalf("expected error for source type %q, got nil", tt.source.Type)
			}
			if !strings.Contains(err.Error(), "cannot handle source type") {
				t.Errorf("error message should mention source type mismatch, got: %v", err)
			}
		})
	}
}

// TestGitHandlerRejectsEmptyURL verifies GitHandler returns error for empty URL.
func TestGitHandlerRejectsEmptyURL(t *testing.T) {
	h := NewGitHandler()
	source := Source{
		Type: SourceTypeGit,
		Git:  GitSource{URL: ""}, // empty URL
	}

	_, err := h.Prepare(context.Background(), source, "/tmp/target")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
	if !strings.Contains(err.Error(), "URL is required") {
		t.Errorf("error message should mention URL requirement, got: %v", err)
	}
}

// TestGitHandlerGitNotFound verifies GitHandler returns GitError when git executable is missing.
func TestGitHandlerGitNotFound(t *testing.T) {
	// This test modifies PATH temporarily to ensure git is not found.
	// It's a realistic test of the exec.LookPath failure path.

	// Skip if we can't reliably remove git from PATH (Windows may have issues).
	if os.Getenv("GOOS") == "windows" {
		t.Skip("PATH manipulation unreliable on Windows")
	}

	// Save original PATH.
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)

	// Set PATH to empty directory (no git).
	tmpDir := t.TempDir()
	os.Setenv("PATH", tmpDir)

	h := NewGitHandler()
	source := Source{
		Type: SourceTypeGit,
		Git:  GitSource{URL: "https://github.com/example/repo.git"},
	}

	_, err := h.Prepare(context.Background(), source, "/tmp/target")
	if err == nil {
		t.Fatal("expected error when git not found, got nil")
	}

	// Verify it's a GitError.
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("expected GitError, got %T: %v", err, err)
	}

	if gitErr.Phase != "lookup" {
		t.Errorf("expected phase 'lookup', got %q", gitErr.Phase)
	}
	if gitErr.URL != source.Git.URL {
		t.Errorf("expected URL %q, got %q", source.Git.URL, gitErr.URL)
	}
	if gitErr.Message == "" {
		t.Error("GitError message should not be empty")
	}
	if !strings.Contains(gitErr.Message, "not found") {
		t.Errorf("message should mention 'not found', got: %q", gitErr.Message)
	}

	// Verify Unwrap returns the underlying exec.LookPathError.
	if gitErr.Err == nil {
		t.Error("GitError.Err should contain underlying error")
	}
}

// TestGitErrorStructure verifies GitError fields and Error() method.
func TestGitErrorStructure(t *testing.T) {
	tests := []struct {
		name     string
		gitErr   *GitError
		wantIn   []string // strings that should appear in Error()
		wantNotIn []string // strings that should NOT appear in Error()
	}{
		{
			name: "clone error with all fields",
			gitErr: &GitError{
				Phase:    "clone",
				URL:      "https://github.com/example/repo.git",
				Ref:      "main",
				ExitCode: 128,
				Message:  "repository not found",
				Err:      errors.New("exit status 128"),
			},
			wantIn: []string{"clone", "url=", "ref=", "exit=", "repository not found", "exit status 128"},
		},
		{
			name: "checkout error",
			gitErr: &GitError{
				Phase:    "checkout",
				URL:      "https://github.com/example/repo.git",
				Ref:      "abc123def456",
				ExitCode: 1,
				Message:  "commit not found",
				Err:      errors.New("exit status 1"),
			},
			wantIn: []string{"checkout", "url=", "ref=", "exit=", "commit not found"},
		},
		{
			name: "lookup error",
			gitErr: &GitError{
				Phase:   "lookup",
				URL:     "https://github.com/example/repo.git",
				Message: "git executable not found",
				Err:     exec.ErrNotFound,
			},
			wantIn: []string{"lookup", "url=", "not found"},
		},
		{
			name: "minimal error",
			gitErr: &GitError{
				Phase:   "clone",
				Message: "failed",
			},
			wantIn: []string{"clone", "failed"},
			wantNotIn: []string{"url=", "ref=", "exit="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.gitErr.Error()

			for _, want := range tt.wantIn {
				if !strings.Contains(errStr, want) {
					t.Errorf("Error() should contain %q, got: %s", want, errStr)
				}
			}

			for _, notWant := range tt.wantNotIn {
				if strings.Contains(errStr, notWant) {
					t.Errorf("Error() should NOT contain %q, got: %s", notWant, errStr)
				}
			}
		})
	}
}

// TestGitErrorUnwrap verifies GitError.Unwrap() works with errors.Is.
func TestGitErrorUnwrap(t *testing.T) {
	underlying := errors.New("underlying error")
	gitErr := &GitError{
		Phase: "clone",
		Err:   underlying,
	}

	// errors.Is should find the underlying error.
	if !errors.Is(gitErr, underlying) {
		t.Errorf("errors.Is(gitErr, underlying) should be true")
	}

	// errors.As should extract the GitError.
	var extracted *GitError
	if !errors.As(gitErr, &extracted) {
		t.Errorf("errors.As should extract GitError")
	}
	if extracted != gitErr {
		t.Errorf("extracted GitError should match original")
	}

	// errors.Is should find the underlying error through Unwrap.
	if !errors.Is(gitErr, underlying) {
		t.Errorf("errors.Is should find underlying error through Unwrap")
	}
}

// TestIsCommitSHA verifies SHA detection logic.
func TestIsCommitSHA(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"", false},                     // empty
		{"main", false},                 // short branch name
		{"feature/foo", false},          // branch name with slash
		{"v1.0.0", false},               // tag
		{"abc123", false},               // short hex (6 chars)
		{"abc123def456789abc123def456789abc123def", false},  // 39 chars (one short)
		{"abc123def456789abc123def456789abc123def4", true},  // 40 chars, valid hex
		{"ABC123DEF456789ABC123DEF456789ABC123DEF4", true},  // 40 chars, uppercase
		{"0123456789abcdef0123456789abcdef01234567", true}, // 40 chars, all hex
		{"0123456789abcdef0123456789abcdef0123456g", false}, // 40 chars with 'g' (not hex)
		{"ghijklmnopqrstuvwxyzghijklmnopqrstuvwxyzgh", false}, // 40 chars, non-hex
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("ref=%q", tt.ref), func(t *testing.T) {
			got := isCommitSHA(tt.ref)
			if got != tt.want {
				t.Errorf("isCommitSHA(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

// TestBuildCloneArgs verifies clone argument construction.
func TestBuildCloneArgs(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		target        string
		ref           string
		depth         int
		useBranchFlag bool
		wantArgs      []string
	}{
		{
			name:          "default clone (no ref, no depth)",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "",
			depth:         0,
			useBranchFlag: true, // should not add --branch since ref is empty
			wantArgs:      []string{"clone", "--single-branch", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "clone with branch",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "main",
			depth:         0,
			useBranchFlag: true,
			wantArgs:      []string{"clone", "--single-branch", "--branch", "main", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "clone with tag",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "v1.0.0",
			depth:         0,
			useBranchFlag: true,
			wantArgs:      []string{"clone", "--single-branch", "--branch", "v1.0.0", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "clone with SHA (useBranchFlag=false)",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "abc123def456789abc123def456789abc123def45",
			depth:         0,
			useBranchFlag: false, // SHA should not use --branch
			wantArgs:      []string{"clone", "--single-branch", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "shallow clone with depth",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "",
			depth:         1,
			useBranchFlag: true,
			wantArgs:      []string{"clone", "--depth", "1", "--single-branch", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "shallow clone with branch and depth",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "develop",
			depth:         10,
			useBranchFlag: true,
			wantArgs:      []string{"clone", "--depth", "10", "--single-branch", "--branch", "develop", "https://github.com/example/repo.git", "/tmp/target"},
		},
		{
			name:          "shallow clone with SHA and depth",
			url:           "https://github.com/example/repo.git",
			target:        "/tmp/target",
			ref:           "abc123def456789abc123def456789abc123def45",
			depth:         5,
			useBranchFlag: false, // SHA should not use --branch even with depth
			wantArgs:      []string{"clone", "--depth", "5", "--single-branch", "https://github.com/example/repo.git", "/tmp/target"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCloneArgs(tt.url, tt.target, tt.ref, tt.depth, tt.useBranchFlag)
			if !equalStrings(got, tt.wantArgs) {
				t.Errorf("buildCloneArgs() = %v, want %v", got, tt.wantArgs)
			}
		})
	}
}

// TestGitHandlerIntegration runs real git operations if git is available.
// These tests require network access and git installed on the system.
func TestGitHandlerIntegration(t *testing.T) {
	// Check if git is available.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping integration tests")
	}

	// Use a small, reliable public repository for testing.
	testRepoURL := "https://github.com/octocat/Hello-World.git"

	h := NewGitHandler()

	t.Run("clone default branch", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo")

		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{URL: testRepoURL},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		path, err := h.Prepare(ctx, source, repoDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != repoDir {
			t.Errorf("returned path %q, want %q", path, repoDir)
		}

		// Verify repo was cloned.
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
			t.Errorf("cloned repo should have .git directory: %v", err)
		}

		// Verify README exists (Hello-World repo has README).
		if _, err := os.Stat(filepath.Join(repoDir, "README")); err != nil {
			t.Errorf("cloned repo should have README: %v", err)
		}
	})

	t.Run("clone with shallow depth", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo-shallow")

		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{
				URL:   testRepoURL,
				Depth: 1,
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		path, err := h.Prepare(ctx, source, repoDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != repoDir {
			t.Errorf("returned path %q, want %q", path, repoDir)
		}

		// Verify shallow clone by checking git log count.
		cmd := exec.Command("git", "rev-list", "--count", "HEAD")
		cmd.Dir = repoDir
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git rev-list failed: %v", err)
		}

		count := strings.TrimSpace(string(output))
		if count != "1" {
			t.Errorf("shallow clone should have 1 commit, got %s", count)
		}
	})

	t.Run("clone with branch ref", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo-branch")

		// Hello-World repo has a "test" branch.
		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{
				URL: testRepoURL,
				Ref: "test",
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		path, err := h.Prepare(ctx, source, repoDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != repoDir {
			t.Errorf("returned path %q, want %q", path, repoDir)
		}

		// Verify current branch.
		cmd := exec.Command("git", "branch", "--show-current")
		cmd.Dir = repoDir
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git branch failed: %v", err)
		}

		branch := strings.TrimSpace(string(output))
		if branch != "test" {
			t.Errorf("expected branch 'test', got %q", branch)
		}
	})

	t.Run("clone with commit SHA", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo-sha")

		// First clone to discover a commit SHA, then use it.
		// Hello-World repo is small, so we can clone it twice.

		// Get a known commit SHA from the repo.
		cmd := exec.Command("git", "ls-remote", testRepoURL, "HEAD")
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git ls-remote failed: %v", err)
		}

		// Parse SHA from ls-remote output (format: SHA\tREF).
		parts := strings.Split(string(output), "\t")
		if len(parts) < 1 {
			t.Fatalf("unexpected ls-remote output: %s", output)
		}
		commitSHA := strings.TrimSpace(parts[0])
		if len(commitSHA) != 40 {
			t.Fatalf("expected 40-char SHA, got %q (len=%d)", commitSHA, len(commitSHA))
		}

		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{
				URL: testRepoURL,
				Ref: commitSHA,
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		path, err := h.Prepare(ctx, source, repoDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != repoDir {
			t.Errorf("returned path %q, want %q", path, repoDir)
		}

		// Verify current commit matches requested SHA.
		cmd = exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repoDir
		output, err = cmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse failed: %v", err)
		}

		currentSHA := strings.TrimSpace(string(output))
		if currentSHA != commitSHA {
			t.Errorf("expected commit %q, got %q", commitSHA, currentSHA)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo-cancel")

		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{URL: testRepoURL},
		}

		// Create a context that we cancel immediately.
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel before starting

		_, err := h.Prepare(ctx, source, repoDir)
		if err == nil {
			t.Fatal("expected error from cancelled context, got nil")
		}

		// Should return context cancellation error.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})

	t.Run("clone invalid URL", func(t *testing.T) {
		target := t.TempDir()
		repoDir := filepath.Join(target, "repo-invalid")

		source := Source{
			Type: SourceTypeGit,
			Git:  GitSource{URL: "https://github.com/nonexistent/repo.git"},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := h.Prepare(ctx, source, repoDir)
		if err == nil {
			t.Fatal("expected error for invalid URL, got nil")
		}

		// Verify it's a GitError with phase "clone".
		var gitErr *GitError
		if !errors.As(err, &gitErr) {
			t.Fatalf("expected GitError, got %T: %v", err, err)
		}

		if gitErr.Phase != "clone" {
			t.Errorf("expected phase 'clone', got %q", gitErr.Phase)
		}
		if gitErr.ExitCode == 0 {
			t.Error("expected non-zero exit code for failed clone")
		}
	})
}

// TestGetExitCode verifies exit code extraction from exec errors.
func TestGetExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "nil error",
			err:      nil,
			wantCode: 0,
		},
		{
			name:     "generic error",
			err:      errors.New("some error"),
			wantCode: 1, // fallback
		},
		{
			name:     "ExitError with code 128",
			err:      &exec.ExitError{ProcessState: &os.ProcessState{}},
			wantCode: 1, // ProcessState doesn't expose exit code in this mock
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getExitCode(tt.err)
			// Note: can't fully test ExitError exit code without real process
			if tt.err == nil && got != 0 {
				t.Errorf("nil error should return 0, got %d", got)
			}
			if tt.err != nil && got == 0 {
				t.Errorf("non-nil error should return non-zero, got %d", got)
			}
		})
	}
}

// equalStrings compares two string slices for equality.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}