package workspace_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/workspace"
	"github.com/stretchr/testify/suite"
)

// ---- isLikelySHA tests ----

type IsLikelySHASuite struct {
	suite.Suite
}

func (s *IsLikelySHASuite) TestValidShortSHA() {
	// 7-character hex string (minimum length).
	s.True(workspace.IsLikelySHA("abc1234"))
}

func (s *IsLikelySHASuite) TestValidFullSHA() {
	// 40-character hex string (full SHA).
	s.True(workspace.IsLikelySHA("abcdef1234567890abcdef1234567890abcdef12"))
}

func (s *IsLikelySHASuite) TestMixedCaseSHA() {
	// Mixed uppercase and lowercase hex.
	s.True(workspace.IsLikelySHA("ABcdEF12"))
}

func (s *IsLikelySHASuite) TestTooShort() {
	// Less than 7 characters.
	s.False(workspace.IsLikelySHA("abc123"))
}

func (s *IsLikelySHASuite) TestTooLong() {
	// More than 40 characters.
	s.False(workspace.IsLikelySHA("abcdef1234567890abcdef1234567890abcdef123"))
}

func (s *IsLikelySHASuite) TestNonHex() {
	// Contains non-hex characters (branch name).
	s.False(workspace.IsLikelySHA("feature/auth"))
}

func (s *IsLikelySHASuite) TestBranchName() {
	// Typical branch name with hyphen.
	s.False(workspace.IsLikelySHA("main"))
	s.False(workspace.IsLikelySHA("develop"))
	s.False(workspace.IsLikelySHA("feature-login"))
}

func (s *IsLikelySHASuite) TestTagName() {
	// Typical tag name.
	s.False(workspace.IsLikelySHA("v1.0.0"))
}

func TestIsLikelySHASuite(t *testing.T) {
	suite.Run(t, new(IsLikelySHASuite))
}

// ---- GitHandler unit tests (mock exec) ----

type GitHandlerMockSuite struct {
	suite.Suite
}

func (s *GitHandlerMockSuite) TestDefaultClone() {
	// Clone without ref or depth.
	var capturedArgs []string
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		s.Equal("git", name)
		s.Equal("", dir) // clone has no working dir
		capturedArgs = args
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: "https://github.com/org/repo.git"},
	}
	targetDir := "/tmp/workspace"

	path, err := mock.Prepare(context.Background(), source, targetDir)
	s.Require().NoError(err)
	s.Equal(targetDir, path)

	// Verify clone args: git clone URL targetDir.
	s.Equal([]string{"clone", "https://github.com/org/repo.git", "/tmp/workspace"}, capturedArgs)
}

func (s *GitHandlerMockSuite) TestDefaultCloneWithDepth() {
	// Clone without ref but with depth.
	var capturedArgs []string
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		capturedArgs = args
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL:   "https://github.com/org/repo.git",
			Depth: 1,
		},
	}

	path, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().NoError(err)
	s.Equal("/tmp/workspace", path)

	// Verify: git clone --depth 1 --single-branch URL targetDir.
	s.Equal([]string{"clone", "--depth", "1", "--single-branch", "https://github.com/org/repo.git", "/tmp/workspace"}, capturedArgs)
}

func (s *GitHandlerMockSuite) TestRefClone() {
	// Clone with branch/tag ref.
	var capturedArgs []string
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		capturedArgs = args
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL: "https://github.com/org/repo.git",
			Ref: "main",
		},
	}

	path, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().NoError(err)
	s.Equal("/tmp/workspace", path)

	// Verify: git clone --branch main --single-branch URL targetDir.
	s.Equal([]string{"clone", "--branch", "main", "--single-branch", "https://github.com/org/repo.git", "/tmp/workspace"}, capturedArgs)
}

func (s *GitHandlerMockSuite) TestRefCloneWithDepth() {
	// Clone with branch ref and depth.
	var capturedArgs []string
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		capturedArgs = args
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL:   "https://github.com/org/repo.git",
			Ref:   "feature/auth",
			Depth: 10,
		},
	}

	path, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().NoError(err)
	s.Equal("/tmp/workspace", path)

	// Verify: git clone --branch feature/auth --single-branch --depth 10 URL targetDir.
	expected := []string{
		"clone", "--branch", "feature/auth", "--single-branch",
		"--depth", "10",
		"https://github.com/org/repo.git", "/tmp/workspace",
	}
	s.Equal(expected, capturedArgs)
}

func (s *GitHandlerMockSuite) TestSHAClone() {
	// Clone with SHA ref: clone then checkout.
	var cloneArgs []string
	var checkoutArgs []string
	var checkoutDir string

	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		if len(args) > 0 && args[0] == "checkout" {
			checkoutArgs = args
			checkoutDir = dir
		} else {
			cloneArgs = args
		}
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL: "https://github.com/org/repo.git",
			Ref: "abc1234", // looks like SHA
		},
	}

	path, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().NoError(err)
	s.Equal("/tmp/workspace", path)

	// Verify clone args: git clone URL targetDir (no --branch, no --single-branch).
	s.Equal([]string{"clone", "https://github.com/org/repo.git", "/tmp/workspace"}, cloneArgs)

	// Verify checkout args: git checkout abc1234.
	s.Equal([]string{"checkout", "abc1234"}, checkoutArgs)
	s.Equal("/tmp/workspace", checkoutDir)
}

func (s *GitHandlerMockSuite) TestSHACloneWithDepth() {
	// Clone with SHA and depth: clone with depth, then checkout.
	var cloneArgs []string
	var checkoutArgs []string

	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		if len(args) > 0 && args[0] == "checkout" {
			checkoutArgs = args
		} else {
			cloneArgs = args
		}
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL:   "https://github.com/org/repo.git",
			Ref:   "abcdef12",
			Depth: 5,
		},
	}

	_, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().NoError(err)

	// Clone with --depth but no --single-branch.
	s.Equal([]string{"clone", "--depth", "5", "https://github.com/org/repo.git", "/tmp/workspace"}, cloneArgs)

	// Checkout still happens.
	s.Equal([]string{"checkout", "abcdef12"}, checkoutArgs)
}

func (s *GitHandlerMockSuite) TestWrongSourceType() {
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeEmptyDir,
	}

	_, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().Error(err)
	s.Contains(err.Error(), "expected git")
}

func (s *GitHandlerMockSuite) TestMissingURL() {
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		return nil
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: ""},
	}

	_, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().Error(err)
	s.Contains(err.Error(), "URL is required")
}

func (s *GitHandlerMockSuite) TestCloneFailure() {
	// Mock returns exit error for clone failure.
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		return &exec.ExitError{Process: nil, Stderr: []byte("fatal: repository not found")}
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: "https://github.com/nonexistent/repo.git"},
	}

	_, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().Error(err)
	s.Contains(err.Error(), "clone failed")
	s.Contains(err.Error(), "https://github.com/nonexistent/repo.git")
}

func (s *GitHandlerMockSuite) TestCheckoutFailure() {
	// Mock succeeds for clone but fails for checkout.
	callCount := 0
	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		callCount++
		if callCount == 1 {
			return nil // clone succeeds
		}
		return &exec.ExitError{Process: nil, Stderr: []byte("error: pathspec 'abc1234' did not match")}
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL: "https://github.com/org/repo.git",
			Ref: "abc1234", // SHA ref triggers checkout
		},
	}

	_, err := mock.Prepare(context.Background(), source, "/tmp/workspace")
	s.Require().Error(err)
	s.Contains(err.Error(), "checkout failed")
	s.Contains(err.Error(), "abc1234")
}

func (s *GitHandlerMockSuite) TestContextCancellation() {
	// Mock returns context.Canceled error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := workspace.NewGitHandlerWithMockExec(func(ctx context.Context, name string, args []string, dir string) error {
		// Check context is cancelled.
		return ctx.Err()
	})

	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: "https://github.com/org/repo.git"},
	}

	_, err := mock.Prepare(ctx, source, "/tmp/workspace")
	s.Require().Error(err)
	s.Contains(err.Error(), "cancelled")
}

func (s *GitHandlerMockSuite) TestParseGitError() {
	// Test structured error parsing.
	err := errors.New("workspace: git clone failed (url=\"https://github.com/org/repo.git\", ref=\"main\", exit=128): some error")
	ge := workspace.ParseGitError(err)
	s.Require().NotNil(ge)
	s.Equal("clone", ge.Phase)
	s.Equal("https://github.com/org/repo.git", ge.URL)
	s.Equal("main", ge.Ref)
	s.Equal(128, ge.ExitCode)
}

func (s *GitHandlerMockSuite) TestParseGitErrorCheckout() {
	// Test checkout error parsing.
	err := errors.New("workspace: git checkout failed (url=\"https://github.com/org/repo.git\", ref=\"abc1234\", exit=1): checkout error")
	ge := workspace.ParseGitError(err)
	s.Require().NotNil(ge)
	s.Equal("checkout", ge.Phase)
	s.Equal("https://github.com/org/repo.git", ge.URL)
	s.Equal("abc1234", ge.Ref)
	s.Equal(1, ge.ExitCode)
}

func (s *GitHandlerMockSuite) TestParseGitErrorNil() {
	// Test nil error parsing.
	ge := workspace.ParseGitError(nil)
	s.Nil(ge)
}

func (s *GitHandlerMockSuite) TestParseGitErrorNonGit() {
	// Test non-git error parsing.
	err := errors.New("some other error")
	ge := workspace.ParseGitError(err)
	s.Nil(ge)
}

func TestGitHandlerMockSuite(t *testing.T) {
	suite.Run(t, new(GitHandlerMockSuite))
}

// ---- GitHandler integration tests (real git) ----

type GitHandlerIntegrationSuite struct {
	suite.Suite
	handler *workspace.GitHandler
}

func (s *GitHandlerIntegrationSuite) SetupSuite() {
	// Create handler if git is available.
	handler, err := workspace.NewGitHandler()
	if err != nil {
		// Skip integration tests if git is not available.
		s.T().Skipf("git not available: %v", err)
	}
	s.handler = handler
}

func (s *GitHandlerIntegrationSuite) TestGitHandlerCreation() {
	// NewGitHandler should succeed if git is in PATH.
	handler, err := workspace.NewGitHandler()
	if err != nil {
		s.T().Skipf("git not available: %v", err)
	}
	s.NotNil(handler)
}

func (s *GitHandlerIntegrationSuite) TestClonePublicRepo() {
	// This test requires network access and git.
	if s.handler == nil {
		s.T().Skip("git handler not available")
	}

	ctx := context.Background()
	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL:   "https://github.com/octocat/Hello-World.git",
			Depth: 1,
		},
	}

	// Create temp target directory.
	targetDir := s.T().TempDir() + "/Hello-World"

	path, err := s.handler.Prepare(ctx, source, targetDir)
	s.Require().NoError(err)
	s.Equal(targetDir, path)

	// Verify README.md exists.
	s.FileExists(targetDir + "/README.md")
}

func (s *GitHandlerIntegrationSuite) TestClonePublicRepoWithRef() {
	// Clone with branch ref.
	if s.handler == nil {
		s.T().Skip("git handler not available")
	}

	ctx := context.Background()
	source := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{
			URL:   "https://github.com/octocat/Hello-World.git",
			Ref:   "master",
			Depth: 1,
		},
	}

	targetDir := s.T().TempDir() + "/Hello-World"

	path, err := s.handler.Prepare(ctx, source, targetDir)
	s.Require().NoError(err)
	s.Equal(targetDir, path)
	s.FileExists(targetDir + "/README.md")
}

func TestGitHandlerIntegrationSuite(t *testing.T) {
	suite.Run(t, new(GitHandlerIntegrationSuite))
}