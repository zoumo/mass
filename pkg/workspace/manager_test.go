// Package workspace implements workspace preparation handlers.
// This file tests the WorkspaceManager implementation.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoumo/mass/pkg/agentd/store"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// TestWorkspaceErrorStructure verifies WorkspaceError has all required fields.
func TestWorkspaceErrorStructure(t *testing.T) {
	underlyingErr := fmt.Errorf("underlying error")
	we := &WorkspaceError{
		Phase:       "prepare-source",
		WorkspaceID: "/tmp/workspace-123",
		SourceType:  SourceTypeGit,
		Managed:     true,
		Message:     "source preparation failed",
		Err:         underlyingErr,
	}

	// Verify all fields are accessible.
	if we.Phase != "prepare-source" {
		t.Errorf("Phase: got %q, want %q", we.Phase, "prepare-source")
	}
	if we.WorkspaceID != "/tmp/workspace-123" {
		t.Errorf("WorkspaceID: got %q, want %q", we.WorkspaceID, "/tmp/workspace-123")
	}
	if we.SourceType != SourceTypeGit {
		t.Errorf("SourceType: got %q, want %q", we.SourceType, SourceTypeGit)
	}
	if we.Managed != true {
		t.Errorf("Managed: got %v, want true", we.Managed)
	}
	if we.Message != "source preparation failed" {
		t.Errorf("Message: got %q, want %q", we.Message, "source preparation failed")
	}
	if !errors.Is(we.Err, underlyingErr) {
		t.Errorf("Err: got %v, want %v", we.Err, underlyingErr)
	}
}

// TestWorkspaceErrorErrorMethod verifies Error() produces formatted string matching GitError/HookError pattern.
func TestWorkspaceErrorErrorMethod(t *testing.T) {
	tests := []struct {
		name     string
		err      *WorkspaceError
		contains []string
	}{
		{
			name: "full error with all fields",
			err: &WorkspaceError{
				Phase:       "prepare-hooks",
				WorkspaceID: "/tmp/ws-456",
				SourceType:  SourceTypeEmptyDir,
				Managed:     true,
				Message:     "setup hooks failed",
				Err:         fmt.Errorf("hook error"),
			},
			contains: []string{
				"workspace: prepare-hooks failed",
				"workspaceID=/tmp/ws-456",
				"sourceType=emptyDir",
				"managed=true",
				"setup hooks failed",
				"error: hook error",
			},
		},
		{
			name: "error with minimal fields",
			err: &WorkspaceError{
				Phase:       "cleanup-source",
				WorkspaceID: "/tmp/ws-min",
				SourceType:  SourceTypeLocal,
				Managed:     false,
			},
			contains: []string{
				"workspace: cleanup-source failed",
				"workspaceID=/tmp/ws-min",
				"sourceType=local",
				"managed=false",
			},
		},
		{
			name: "error with empty workspaceID",
			err: &WorkspaceError{
				Phase:       "prepare-source",
				WorkspaceID: "",
				SourceType:  SourceTypeGit,
				Managed:     true,
				Message:     "validation failed",
			},
			contains: []string{
				"workspace: prepare-source failed",
				"sourceType=git",
				"managed=true",
				"validation failed",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()
			for _, want := range tt.contains {
				if !strings.Contains(errStr, want) {
					t.Errorf("Error() missing expected substring %q, got: %s", want, errStr)
				}
			}
		})
	}
}

// TestWorkspaceErrorUnwrap verifies Unwrap() returns Err for errors.Is/errors.As compatibility.
func TestWorkspaceErrorUnwrap(t *testing.T) {
	underlyingErr := fmt.Errorf("original error")
	we := &WorkspaceError{
		Phase:       "prepare-source",
		WorkspaceID: "/tmp/ws",
		SourceType:  SourceTypeGit,
		Managed:     true,
		Message:     "test",
		Err:         underlyingErr,
	}

	// Unwrap should return underlying error.
	unwrapped := we.Unwrap()
	if !errors.Is(unwrapped, underlyingErr) {
		t.Errorf("Unwrap(): got %v, want %v", unwrapped, underlyingErr)
	}

	// errors.Is should work through the chain.
	if !errors.Is(we, underlyingErr) {
		t.Errorf("errors.Is(we, underlyingErr) should return true")
	}

	// errors.Unwrap chain should reach the original error.
	chain := errors.Unwrap(we)
	if !errors.Is(chain, underlyingErr) {
		t.Errorf("errors.Unwrap(we): got %v, want %v", chain, underlyingErr)
	}
}

// TestNewWorkspaceManager verifies constructor initializes handlers for all 3 source types.
func TestNewWorkspaceManager(t *testing.T) {
	m := NewWorkspaceManager()

	// Verify handlers map has all 3 source types.
	expectedTypes := []SourceType{SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal}
	for _, st := range expectedTypes {
		if _, ok := m.handlers[st]; !ok {
			t.Errorf("handlers missing for source type %q", st)
		}
	}

	// Verify hookExecutor is initialized.
	if m.hookExecutor == nil {
		t.Error("hookExecutor should be initialized")
	}

	// Verify refCount map is initialized.
	if m.refCount == nil {
		t.Error("refCount map should be initialized")
	}

	// Verify refCount is empty initially.
	if len(m.refCount) != 0 {
		t.Errorf("refCount should be empty initially, got %d entries", len(m.refCount))
	}
}

// TestWorkspaceManagerPrepareGitSource verifies Prepare routes to GitHandler correctly.
func TestWorkspaceManagerPrepareGitSource(t *testing.T) {
	t.Run("valid git spec prepares workspace", func(t *testing.T) {
		// Skip if git not available.
		if _, err := os.Stat("/usr/bin/git"); err != nil {
			t.Skip("git not available")
		}

		m := NewWorkspaceManager()
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "git-workspace")

		// Use a small, fast repository for testing.
		spec := WorkspaceSpec{
			MassVersion: "0.1.0",
			Metadata:    WorkspaceMetadata{Name: "test-git-workspace"},
			Source: Source{
				Type: SourceTypeGit,
				Git:  GitSource{URL: "https://github.com/octocat/Hello-World.git", Depth: 1},
			},
		}

		path, err := m.Prepare(context.Background(), spec, targetDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		// Should return targetDir for managed git workspace.
		if path != targetDir {
			t.Errorf("returned path %q, want %q", path, targetDir)
		}

		// Verify workspace was cloned.
		if _, err := os.Stat(targetDir); err != nil {
			t.Errorf("workspace directory should exist: %v", err)
		}

		// Verify ref count was incremented.
		m.mutex.Lock()
		count := m.refCount[targetDir]
		m.mutex.Unlock()
		if count != 1 {
			t.Errorf("refCount[%q] = %d, want 1", targetDir, count)
		}
	})

	t.Run("git spec validation failure", func(t *testing.T) {
		m := NewWorkspaceManager()
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "git-invalid")

		// Spec missing required git URL.
		spec := WorkspaceSpec{
			MassVersion: "0.1.0",
			Metadata:    WorkspaceMetadata{Name: "invalid-git"},
			Source: Source{
				Type: SourceTypeGit,
				Git:  GitSource{URL: ""}, // Missing required URL
			},
		}

		_, err := m.Prepare(context.Background(), spec, targetDir)
		if err == nil {
			t.Fatal("expected error for invalid spec, got nil")
		}

		// Should be WorkspaceError.
		var we *WorkspaceError
		if !errors.As(err, &we) {
			t.Fatalf("expected WorkspaceError, got %T: %v", err, err)
		}

		// Verify error fields.
		if we.Phase != "prepare-source" {
			t.Errorf("Phase: got %q, want %q", we.Phase, "prepare-source")
		}
		if we.Managed != true {
			t.Errorf("Managed for git should be true, got false")
		}
	})
}

// TestWorkspaceManagerPrepareEmptyDirSource verifies Prepare routes to EmptyDirHandler correctly.
func TestWorkspaceManagerPrepareEmptyDirSource(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "empty-workspace")

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-empty-workspace"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
	}

	path, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}

	// Should return targetDir for managed emptyDir workspace.
	if path != targetDir {
		t.Errorf("returned path %q, want %q", path, targetDir)
	}

	// Verify workspace was created.
	if _, err := os.Stat(targetDir); err != nil {
		t.Errorf("workspace directory should exist: %v", err)
	}

	// Verify ref count was incremented.
	m.mutex.Lock()
	count := m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("refCount[%q] = %d, want 1", targetDir, count)
	}

	// Verify Managed flag is true for emptyDir.
	// (Implicitly verified by cleanup behavior tests)
}

// TestWorkspaceManagerPrepareLocalSource verifies Prepare routes to LocalHandler correctly.
func TestWorkspaceManagerPrepareLocalSource(t *testing.T) {
	m := NewWorkspaceManager()

	// Create a real local directory.
	localDir := t.TempDir()

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-local-workspace"},
		Source: Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: localDir},
		},
	}

	// Note: LocalHandler returns source.Local.Path, NOT targetDir.
	// The targetDir parameter is unused for local sources.
	path, err := m.Prepare(context.Background(), spec, "/some/unused/target")
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}

	// Should return source.Local.Path, NOT targetDir.
	if path != localDir {
		t.Errorf("returned path %q, want %q (local source path)", path, localDir)
	}

	// Local workspace should NOT be tracked in refCount (unmanaged).
	// RefCount should track the workspaceID passed to Acquire (targetDir).
	// Since local sources return the source path, we need to check what was acquired.
	m.mutex.Lock()
	count := m.refCount["/some/unused/target"]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("refCount should track the workspaceID passed to Acquire")
	}
}

// TestWorkspaceManagerPrepareInvalidSpec verifies Prepare returns WorkspaceError for invalid spec.
func TestWorkspaceManagerPrepareInvalidSpec(t *testing.T) {
	m := NewWorkspaceManager()

	tests := []struct {
		name    string
		spec    WorkspaceSpec
		wantErr string
	}{
		{
			name: "missing massVersion",
			spec: WorkspaceSpec{
				MassVersion: "",
				Metadata:    WorkspaceMetadata{Name: "test"},
				Source:      Source{Type: SourceTypeEmptyDir},
			},
			wantErr: "massVersion is required",
		},
		{
			name: "missing metadata.name",
			spec: WorkspaceSpec{
				MassVersion: "0.1.0",
				Metadata:    WorkspaceMetadata{Name: ""},
				Source:      Source{Type: SourceTypeEmptyDir},
			},
			wantErr: "metadata.name is required",
		},
		{
			name: "invalid source type",
			spec: WorkspaceSpec{
				MassVersion: "0.1.0",
				Metadata:    WorkspaceMetadata{Name: "test"},
				Source:      Source{Type: SourceType("invalid")},
			},
			wantErr: "source.type",
		},
		{
			name: "unsupported major version",
			spec: WorkspaceSpec{
				MassVersion: "1.0.0",
				Metadata:    WorkspaceMetadata{Name: "test"},
				Source:      Source{Type: SourceTypeEmptyDir},
			},
			wantErr: "unsupported massVersion major",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.Prepare(context.Background(), tt.spec, "/tmp/target")
			if err == nil {
				t.Fatal("expected error for invalid spec, got nil")
			}

			// Should be WorkspaceError.
			var we *WorkspaceError
			if !errors.As(err, &we) {
				t.Fatalf("expected WorkspaceError, got %T: %v", err, err)
			}

			// Verify phase is prepare-source.
			if we.Phase != "prepare-source" {
				t.Errorf("Phase: got %q, want %q", we.Phase, "prepare-source")
			}

			// Verify error message contains expected validation error.
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error should contain %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestWorkspaceManagerPrepareHookFailureCleanup verifies setup hook failure triggers cleanup for managed workspaces.
func TestWorkspaceManagerPrepareHookFailureCleanup(t *testing.T) {
	m := NewWorkspaceManager()

	t.Run("managed workspace cleans up on hook failure", func(t *testing.T) {
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "managed-ws")

		// Create a hook that will fail.
		spec := WorkspaceSpec{
			MassVersion: "0.1.0",
			Metadata:    WorkspaceMetadata{Name: "hook-fail-test"},
			Source: Source{
				Type:     SourceTypeEmptyDir,
				EmptyDir: EmptyDirSource{},
			},
			Hooks: Hooks{
				Setup: []Hook{
					{Command: "false", Description: "hook that always fails"},
				},
			},
		}

		_, err := m.Prepare(context.Background(), spec, targetDir)
		if err == nil {
			t.Fatal("expected error from hook failure, got nil")
		}

		// Should be WorkspaceError.
		var we *WorkspaceError
		if !errors.As(err, &we) {
			t.Fatalf("expected WorkspaceError, got %T: %v", err, err)
		}

		// Verify phase is prepare-hooks.
		if we.Phase != "prepare-hooks" {
			t.Errorf("Phase: got %q, want %q", we.Phase, "prepare-hooks")
		}

		// Verify Managed flag is true for emptyDir.
		if we.Managed != true {
			t.Errorf("Managed for emptyDir should be true")
		}

		// CRITICAL: Verify cleanup happened - targetDir should NOT exist.
		if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
			t.Errorf("managed workspace should be cleaned up on hook failure, but directory exists: %v", err)
		}

		// Verify ref count was NOT incremented (cleanup happened before Acquire).
		m.mutex.Lock()
		count := m.refCount[targetDir]
		m.mutex.Unlock()
		if count != 0 {
			t.Errorf("refCount[%q] = %d, want 0 (cleanup before acquire)", targetDir, count)
		}
	})

	t.Run("unmanaged workspace does NOT cleanup on hook failure", func(t *testing.T) {
		// Create a local directory that exists.
		localDir := t.TempDir()

		// Create a file in the local directory to verify it persists.
		testFile := filepath.Join(localDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		spec := WorkspaceSpec{
			MassVersion: "0.1.0",
			Metadata:    WorkspaceMetadata{Name: "local-hook-fail"},
			Source: Source{
				Type:  SourceTypeLocal,
				Local: LocalSource{Path: localDir},
			},
			Hooks: Hooks{
				Setup: []Hook{
					{Command: "false", Description: "hook that always fails"},
				},
			},
		}

		_, err := m.Prepare(context.Background(), spec, "/unused/target")
		if err == nil {
			t.Fatal("expected error from hook failure, got nil")
		}

		// Should be WorkspaceError.
		var we *WorkspaceError
		if !errors.As(err, &we) {
			t.Fatalf("expected WorkspaceError, got %T: %v", err, err)
		}

		// Verify Managed flag is false for local.
		if we.Managed != false {
			t.Errorf("Managed for local should be false")
		}

		// CRITICAL: Verify cleanup did NOT happen - localDir should still exist.
		if _, err := os.Stat(localDir); err != nil {
			t.Errorf("local workspace should NOT be cleaned up, but directory is gone: %v", err)
		}

		// Verify the test file still exists.
		if _, err := os.Stat(testFile); err != nil {
			t.Errorf("local workspace contents should NOT be deleted: %v", err)
		}
	})
}

// TestWorkspaceManagerAcquire verifies Acquire increments refCount correctly.
func TestWorkspaceManagerAcquire(t *testing.T) {
	m := NewWorkspaceManager()

	workspaceID := "/tmp/test-workspace"

	// Initially 0 (not in map).
	m.mutex.Lock()
	count := m.refCount[workspaceID]
	m.mutex.Unlock()
	if count != 0 {
		t.Errorf("initial refCount should be 0, got %d", count)
	}

	// First acquire: 0 → 1.
	m.Acquire(workspaceID)
	m.mutex.Lock()
	count = m.refCount[workspaceID]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("after first Acquire: refCount = %d, want 1", count)
	}

	// Second acquire: 1 → 2.
	m.Acquire(workspaceID)
	m.mutex.Lock()
	count = m.refCount[workspaceID]
	m.mutex.Unlock()
	if count != 2 {
		t.Errorf("after second Acquire: refCount = %d, want 2", count)
	}

	// Acquire different workspace.
	workspaceID2 := "/tmp/other-workspace"
	m.Acquire(workspaceID2)
	m.mutex.Lock()
	count = m.refCount[workspaceID2]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("new workspace refCount = %d, want 1", count)
	}

	// Original workspace count unchanged.
	m.mutex.Lock()
	count = m.refCount[workspaceID]
	m.mutex.Unlock()
	if count != 2 {
		t.Errorf("original workspace refCount should still be 2, got %d", count)
	}
}

// TestIsManaged verifies isManaged helper returns correct values for each source type.
func TestIsManaged(t *testing.T) {
	tests := []struct {
		name     string
		source   Source
		expected bool
	}{
		{
			name:     "git source is managed",
			source:   Source{Type: SourceTypeGit},
			expected: true,
		},
		{
			name:     "emptyDir source is managed",
			source:   Source{Type: SourceTypeEmptyDir},
			expected: true,
		},
		{
			name:     "local source is NOT managed",
			source:   Source{Type: SourceTypeLocal},
			expected: false,
		},
		{
			name:     "unknown source type is NOT managed",
			source:   Source{Type: SourceType("unknown")},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isManaged(tt.source)
			if result != tt.expected {
				t.Errorf("isManaged(%q) = %v, want %v", tt.source.Type, result, tt.expected)
			}
		})
	}
}

// TestWorkspaceManagerRelease verifies Release decrements refCount correctly.
func TestWorkspaceManagerRelease(t *testing.T) {
	m := NewWorkspaceManager()
	workspaceID := "/tmp/test-workspace"

	// Setup: Acquire twice (count = 2).
	m.Acquire(workspaceID)
	m.Acquire(workspaceID)

	// Verify initial count.
	m.mutex.Lock()
	count := m.refCount[workspaceID]
	m.mutex.Unlock()
	if count != 2 {
		t.Fatalf("initial refCount should be 2, got %d", count)
	}

	// First release: 2 → 1.
	count = m.Release(workspaceID)
	if count != 1 {
		t.Errorf("after first Release: returned count = %d, want 1", count)
	}
	m.mutex.Lock()
	actualCount := m.refCount[workspaceID]
	m.mutex.Unlock()
	if actualCount != 1 {
		t.Errorf("after first Release: refCount = %d, want 1", actualCount)
	}

	// Second release: 1 → 0.
	count = m.Release(workspaceID)
	if count != 0 {
		t.Errorf("after second Release: returned count = %d, want 0", count)
	}
	m.mutex.Lock()
	actualCount = m.refCount[workspaceID]
	m.mutex.Unlock()
	if actualCount != 0 {
		t.Errorf("after second Release: refCount = %d, want 0", actualCount)
	}

	// Release when count is 0: should stay at 0.
	count = m.Release(workspaceID)
	if count != 0 {
		t.Errorf("Release when count=0: returned count = %d, want 0", count)
	}

	// Release for unknown workspace: should return 0.
	unknownID := "/tmp/unknown-workspace"
	count = m.Release(unknownID)
	if count != 0 {
		t.Errorf("Release for unknown workspace: returned count = %d, want 0", count)
	}
}

// TestWorkspaceManagerLifecycleGit verifies Prepare → Cleanup round-trip for Git source.
func TestWorkspaceManagerLifecycleGit(t *testing.T) {
	// Skip if git not available.
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git not available")
	}

	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "git-workspace")

	// Use a small, fast repository for testing.
	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-git-lifecycle"},
		Source: Source{
			Type: SourceTypeGit,
			Git:  GitSource{URL: "https://github.com/octocat/Hello-World.git", Depth: 1},
		},
	}

	// Prepare: creates workspace.
	path, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}
	if path != targetDir {
		t.Errorf("Prepare returned path %q, want %q", path, targetDir)
	}

	// Verify workspace exists.
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("workspace directory should exist after Prepare: %v", err)
	}

	// Verify ref count is 1.
	m.mutex.Lock()
	count := m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("refCount after Prepare = %d, want 1", count)
	}

	// Cleanup: deletes managed workspace.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("Cleanup() failed: %v", err)
	}

	// Verify workspace directory is deleted.
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("managed workspace should be deleted after Cleanup, but directory exists")
	}

	// Verify ref count is 0.
	m.mutex.Lock()
	count = m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 0 {
		t.Errorf("refCount after Cleanup = %d, want 0", count)
	}
}

// TestWorkspaceManagerLifecycleEmptyDir verifies Prepare → Cleanup round-trip for EmptyDir source.
func TestWorkspaceManagerLifecycleEmptyDir(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "empty-workspace")

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-emptydir-lifecycle"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
	}

	// Prepare: creates workspace.
	path, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}
	if path != targetDir {
		t.Errorf("Prepare returned path %q, want %q", path, targetDir)
	}

	// Verify workspace exists.
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("workspace directory should exist after Prepare: %v", err)
	}

	// Cleanup: deletes managed workspace.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("Cleanup() failed: %v", err)
	}

	// Verify workspace directory is deleted.
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("managed workspace should be deleted after Cleanup, but directory exists")
	}
}

// TestWorkspaceManagerLifecycleLocal verifies Local workspace NOT deleted on Cleanup.
func TestWorkspaceManagerLifecycleLocal(t *testing.T) {
	m := NewWorkspaceManager()

	// Create a real local directory.
	localDir := t.TempDir()

	// Create a file to verify it persists.
	testFile := filepath.Join(localDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-local-lifecycle"},
		Source: Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: localDir},
		},
	}

	// Prepare: validates local workspace.
	path, err := m.Prepare(context.Background(), spec, "/unused/target")
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}
	if path != localDir {
		t.Errorf("Prepare returned path %q, want %q (local path)", path, localDir)
	}

	// Verify local directory exists.
	if _, err := os.Stat(localDir); err != nil {
		t.Fatalf("local directory should exist after Prepare: %v", err)
	}

	// Cleanup: should NOT delete local workspace.
	// Note: Cleanup uses workspaceID (targetDir passed to Prepare), not localDir.
	err = m.Cleanup(context.Background(), "/unused/target", spec)
	if err != nil {
		t.Fatalf("Cleanup() failed: %v", err)
	}

	// CRITICAL: Verify local workspace STILL exists.
	if _, err := os.Stat(localDir); err != nil {
		t.Errorf("local workspace should NOT be deleted after Cleanup, but directory is gone: %v", err)
	}

	// Verify test file still exists.
	if _, err := os.Stat(testFile); err != nil {
		t.Errorf("local workspace contents should NOT be deleted: %v", err)
	}
}

// TestWorkspaceManagerReferenceCounting verifies Cleanup only triggered when count reaches zero.
func TestWorkspaceManagerReferenceCounting(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "refcount-workspace")

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-refcount"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
	}

	// Prepare: creates workspace, ref count = 1.
	_, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}

	// Acquire again: ref count = 2.
	m.Acquire(targetDir)

	// Verify ref count is 2.
	m.mutex.Lock()
	count := m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 2 {
		t.Fatalf("refCount after second Acquire = %d, want 2", count)
	}

	// First Cleanup: Release → count=1, cleanup NOT triggered.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("first Cleanup() failed: %v", err)
	}

	// Verify workspace still exists (cleanup not triggered).
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("workspace should still exist after first Cleanup (count=1): %v", err)
	}

	// Verify ref count is 1.
	m.mutex.Lock()
	count = m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("refCount after first Cleanup = %d, want 1", count)
	}

	// Second Cleanup: Release → count=0, cleanup triggered.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("second Cleanup() failed: %v", err)
	}

	// Verify workspace is deleted (cleanup triggered).
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("workspace should be deleted after second Cleanup (count=0)")
	}

	// Verify ref count is 0.
	m.mutex.Lock()
	count = m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 0 {
		t.Errorf("refCount after second Cleanup = %d, want 0", count)
	}
}

// TestWorkspaceManagerCleanupHookFailure verifies teardown hook failure does not prevent cleanup.
func TestWorkspaceManagerCleanupHookFailure(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "hook-fail-cleanup")

	// Setup spec with failing teardown hook.
	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-cleanup-hook-fail"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
		Hooks: Hooks{
			Teardown: []Hook{
				{Command: "false", Description: "teardown hook that always fails"},
			},
		},
	}

	// Prepare: creates workspace.
	_, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}

	// Verify workspace exists.
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("workspace should exist after Prepare: %v", err)
	}

	// Cleanup: teardown hook fails, but cleanup continues (best-effort).
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("Cleanup() should not fail even if teardown hooks fail, got: %v", err)
	}

	// CRITICAL: Verify workspace is deleted despite hook failure.
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("managed workspace should be deleted even when teardown hook fails")
	}
}

// TestWorkspaceManagerPrepareHookFailureCleanupManaged verifies setup hook failure triggers cleanup.
func TestWorkspaceManagerPrepareHookFailureCleanupManaged(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "prepare-hook-fail-managed")

	// Setup spec with failing setup hook.
	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-prepare-hook-fail-managed"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
		Hooks: Hooks{
			Setup: []Hook{
				{Command: "false", Description: "setup hook that always fails"},
			},
		},
	}

	// Prepare: setup hook fails, cleanup should happen.
	_, err := m.Prepare(context.Background(), spec, targetDir)
	if err == nil {
		t.Fatal("expected error from setup hook failure, got nil")
	}

	// Verify it's a WorkspaceError.
	var we *WorkspaceError
	if !errors.As(err, &we) {
		t.Fatalf("expected WorkspaceError, got %T: %v", err, err)
	}

	// Verify phase is prepare-hooks.
	if we.Phase != "prepare-hooks" {
		t.Errorf("Phase: got %q, want %q", we.Phase, "prepare-hooks")
	}

	// Verify Managed is true for emptyDir.
	if we.Managed != true {
		t.Errorf("Managed for emptyDir should be true")
	}

	// CRITICAL: Verify workspace directory was cleaned up (not left behind).
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("managed workspace should be cleaned up on prepare hook failure, but directory exists")
	}

	// Verify ref count was NOT incremented (cleanup before Acquire).
	m.mutex.Lock()
	count := m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 0 {
		t.Errorf("refCount[%q] = %d, want 0 (cleanup happened before Acquire)", targetDir, count)
	}
}

// TestWorkspaceManagerMultipleSessions verifies multiple sessions sharing a workspace.
func TestWorkspaceManagerMultipleSessions(t *testing.T) {
	m := NewWorkspaceManager()
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "multi-session-workspace")

	spec := WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    WorkspaceMetadata{Name: "test-multi-session"},
		Source: Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		},
	}

	// Session 1: Prepare → ref count = 1.
	_, err := m.Prepare(context.Background(), spec, targetDir)
	if err != nil {
		t.Fatalf("Prepare() for session 1 failed: %v", err)
	}

	// Session 2: Acquire same workspace → ref count = 2.
	m.Acquire(targetDir)

	// Verify ref count is 2.
	m.mutex.Lock()
	count := m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 2 {
		t.Fatalf("refCount after session 2 Acquire = %d, want 2", count)
	}

	// Session 1 Cleanup: Release → count=1, workspace NOT deleted.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("Cleanup() for session 1 failed: %v", err)
	}

	// Verify workspace still exists.
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("workspace should still exist after session 1 Cleanup (count=1): %v", err)
	}

	// Verify ref count is 1.
	m.mutex.Lock()
	count = m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 1 {
		t.Errorf("refCount after session 1 Cleanup = %d, want 1", count)
	}

	// Session 2 Cleanup: Release → count=0, workspace deleted.
	err = m.Cleanup(context.Background(), targetDir, spec)
	if err != nil {
		t.Fatalf("Cleanup() for session 2 failed: %v", err)
	}

	// Verify workspace is deleted.
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("workspace should be deleted after session 2 Cleanup (count=0)")
	}

	// Verify ref count is 0.
	m.mutex.Lock()
	count = m.refCount[targetDir]
	m.mutex.Unlock()
	if count != 0 {
		t.Errorf("refCount after session 2 Cleanup = %d, want 0", count)
	}
}

// TestWorkspaceManagerInitRefCounts verifies InitRefCounts seeds the in-memory
// refCount map from the metadata store for all ready workspaces.
// In the new bbolt model, ref counts are not persisted in DB;
// InitRefCounts pre-registers paths at count=0 so cleanup logic works correctly.
func TestWorkspaceManagerInitRefCounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	metaStore, err := store.NewStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer metaStore.Close()

	ctx := context.Background()

	srcJSON, _ := json.Marshal(Source{Type: SourceTypeEmptyDir})

	// Workspace 1: phase=ready with a path — should appear in refCount map.
	ws1 := &pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "ws-ready-a"},
		Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
		Status:   pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhaseReady, Path: "/var/workspaces/ws-ready-a"},
	}
	if err := metaStore.CreateWorkspace(ctx, ws1); err != nil {
		t.Fatalf("CreateWorkspace ws-ready-a: %v", err)
	}

	// Workspace 2: phase=ready with a path — also appears.
	ws2 := &pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "ws-ready-b"},
		Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
		Status:   pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhaseReady, Path: "/var/workspaces/ws-ready-b"},
	}
	if err := metaStore.CreateWorkspace(ctx, ws2); err != nil {
		t.Fatalf("CreateWorkspace ws-ready-b: %v", err)
	}

	// Workspace 3: phase=pending — NOT included in the init (only ready workspaces are loaded).
	ws3 := &pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "ws-pending"},
		Status:   pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhasePending},
	}
	if err := metaStore.CreateWorkspace(ctx, ws3); err != nil {
		t.Fatalf("CreateWorkspace ws-pending: %v", err)
	}

	// Init refcounts from DB.
	m := NewWorkspaceManager()
	if err := m.InitRefCounts(metaStore); err != nil {
		t.Fatalf("InitRefCounts failed: %v", err)
	}

	// Verify ws-ready-a path is seeded at 0.
	m.mutex.Lock()
	countA, okA := m.refCount["/var/workspaces/ws-ready-a"]
	countB, okB := m.refCount["/var/workspaces/ws-ready-b"]
	m.mutex.Unlock()

	if !okA {
		t.Error("ws-ready-a path not present in refCount map after InitRefCounts")
	}
	if countA != 0 {
		t.Errorf("ws-ready-a refCount = %d, want 0 (seeded from DB, incremented at runtime)", countA)
	}
	if !okB {
		t.Error("ws-ready-b path not present in refCount map after InitRefCounts")
	}
	if countB != 0 {
		t.Errorf("ws-ready-b refCount = %d, want 0", countB)
	}

	// Verify Acquire increments from the seeded baseline.
	m.Acquire("/var/workspaces/ws-ready-a")
	m.Acquire("/var/workspaces/ws-ready-a")
	m.mutex.Lock()
	countAfterAcquire := m.refCount["/var/workspaces/ws-ready-a"]
	m.mutex.Unlock()
	if countAfterAcquire != 2 {
		t.Errorf("after 2 Acquire calls: refCount = %d, want 2", countAfterAcquire)
	}

	// Pending workspace path should NOT be in the map (no path field set).
	m.mutex.Lock()
	_, okPending := m.refCount[""]
	m.mutex.Unlock()
	// Empty path should not be keyed (InitRefCounts skips empty paths).
	_ = okPending // not asserted strictly since empty string is not a valid path key
}
