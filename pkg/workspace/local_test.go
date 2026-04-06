// Package workspace implements workspace preparation handlers.
// This file tests the LocalHandler implementation.
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalHandlerRejectsNonLocalSource verifies LocalHandler returns error for wrong source type.
func TestLocalHandlerRejectsNonLocalSource(t *testing.T) {
	h := NewLocalHandler()

	tests := []struct {
		name   string
		source Source
	}{
		{
			name: "git source",
			source: Source{
				Type: SourceTypeGit,
				Git:  GitSource{URL: "https://github.com/example/repo.git"},
			},
		},
		{
			name: "emptyDir source",
			source: Source{
				Type:     SourceTypeEmptyDir,
				EmptyDir: EmptyDirSource{},
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

// TestLocalHandlerPathDoesNotExist verifies LocalHandler returns error for non-existent path.
func TestLocalHandlerPathDoesNotExist(t *testing.T) {
	h := NewLocalHandler()

	// Use a path that definitely doesn't exist.
	nonExistentPath := "/tmp/this-path-definitely-does-not-exist-12345"

	source := Source{
		Type:  SourceTypeLocal,
		Local: LocalSource{Path: nonExistentPath},
	}

	_, err := h.Prepare(context.Background(), source, "/tmp/target")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}

	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error message should mention 'does not exist', got: %v", err)
	}

	// Verify the path is mentioned in the error.
	if !strings.Contains(err.Error(), nonExistentPath) {
		t.Errorf("error message should mention the path, got: %v", err)
	}
}

// TestLocalHandlerPathIsFile verifies LocalHandler returns error when path is a file (not directory).
func TestLocalHandlerPathIsFile(t *testing.T) {
	h := NewLocalHandler()

	// Create a temporary file (not directory).
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "some-file.txt")

	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	f.Close()

	source := Source{
		Type:  SourceTypeLocal,
		Local: LocalSource{Path: filePath},
	}

	_, err = h.Prepare(context.Background(), source, "/tmp/target")
	if err == nil {
		t.Fatal("expected error when path is a file, got nil")
	}

	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error message should mention 'not a directory', got: %v", err)
	}

	// Verify the path is mentioned in the error.
	if !strings.Contains(err.Error(), filePath) {
		t.Errorf("error message should mention the path, got: %v", err)
	}
}

// TestLocalHandlerIntegration verifies LocalHandler works with real directories.
func TestLocalHandlerIntegration(t *testing.T) {
	h := NewLocalHandler()

	t.Run("returns source path not targetDir", func(t *testing.T) {
		// Create an existing directory to use as local source.
		localDir := t.TempDir()

		// targetDir should NOT be returned - we verify this by using a different path.
		targetDir := filepath.Join(t.TempDir(), "target-should-not-be-used")

		source := Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: localDir},
		}

		path, err := h.Prepare(context.Background(), source, targetDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		// CRITICAL: Should return source.Local.Path, NOT targetDir.
		if path != localDir {
			t.Errorf("returned path %q, want %q (source.Local.Path, NOT targetDir)", path, localDir)
		}

		if path == targetDir {
			t.Errorf("should NOT return targetDir %q, but did", targetDir)
		}
	})

	t.Run("validates directory exists", func(t *testing.T) {
		localDir := t.TempDir()

		// Verify the temp dir exists.
		if _, err := os.Stat(localDir); err != nil {
			t.Fatalf("test setup failed: temp dir should exist: %v", err)
		}

		source := Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: localDir},
		}

		path, err := h.Prepare(context.Background(), source, "/tmp/target")
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != localDir {
			t.Errorf("returned path %q, want %q", path, localDir)
		}

		// Verify the directory still exists (no modification).
		info, err := os.Stat(localDir)
		if err != nil {
			t.Errorf("local directory should still exist: %v", err)
		}

		if !info.IsDir() {
			t.Errorf("local path should still be a directory")
		}
	})

	t.Run("works with nested directories", func(t *testing.T) {
		tmpRoot := t.TempDir()
		nestedDir := filepath.Join(tmpRoot, "nested", "deep", "path")

		// Create nested directory.
		if err := os.MkdirAll(nestedDir, 0755); err != nil {
			t.Fatalf("failed to create nested directory: %v", err)
		}

		source := Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: nestedDir},
		}

		path, err := h.Prepare(context.Background(), source, "/tmp/target")
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != nestedDir {
			t.Errorf("returned path %q, want %q", path, nestedDir)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		localDir := t.TempDir()

		source := Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: localDir},
		}

		// Create a cancelled context.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := h.Prepare(ctx, source, "/tmp/target")
		if err == nil {
			t.Fatal("expected error from cancelled context, got nil")
		}

		// Should return context cancellation error.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		// Skip on Windows where permission bits work differently.
		if os.Getenv("GOOS") == "windows" {
			t.Skip("permission test unreliable on Windows")
		}

		tmpRoot := t.TempDir()
		restrictedDir := filepath.Join(tmpRoot, "restricted")

		// Create directory with no read permissions.
		if err := os.Mkdir(restrictedDir, 0000); err != nil {
			t.Fatalf("failed to create restricted directory: %v", err)
		}

		// Restore permissions after test so cleanup can work.
		defer os.Chmod(restrictedDir, 0755)

		source := Source{
			Type:  SourceTypeLocal,
			Local: LocalSource{Path: restrictedDir},
		}

		_, err := h.Prepare(context.Background(), source, "/tmp/target")
		// The behavior here depends on the OS:
		// - On Linux, os.Stat may succeed but os.ReadDir would fail
		// - The handler only uses os.Stat, so this might pass
		// We're testing that if os.Stat fails, we get a wrapped error.
		if err != nil {
			// If we got an error, it should be wrapped.
			if !strings.Contains(err.Error(), "failed to stat") {
				t.Errorf("error should mention stat failure, got: %v", err)
			}
		}
		// If no error, that's also acceptable - os.Stat might succeed.
	})
}