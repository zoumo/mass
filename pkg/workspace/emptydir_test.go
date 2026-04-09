// Package workspace implements workspace preparation handlers.
// This file tests the EmptyDirHandler implementation.
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmptyDirHandlerRejectsNonEmptyDirSource verifies EmptyDirHandler returns error for wrong source type.
func TestEmptyDirHandlerRejectsNonEmptyDirSource(t *testing.T) {
	h := NewEmptyDirHandler()

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

// TestEmptyDirHandlerIntegration verifies EmptyDirHandler creates directories correctly.
func TestEmptyDirHandlerIntegration(t *testing.T) {
	h := NewEmptyDirHandler()

	t.Run("creates empty directory", func(t *testing.T) {
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "workspace")

		source := Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		}

		path, err := h.Prepare(context.Background(), source, targetDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != targetDir {
			t.Errorf("returned path %q, want %q", path, targetDir)
		}

		// Verify directory exists.
		info, err := os.Stat(targetDir)
		if err != nil {
			t.Fatalf("directory should exist: %v", err)
		}

		if !info.IsDir() {
			t.Errorf("expected directory, got file")
		}

		// Verify permissions (0755 = rwxr-xr-x).
		expectedPerm := os.FileMode(0o755)
		if info.Mode().Perm() != expectedPerm {
			t.Errorf("expected permissions %04o, got %04o", expectedPerm, info.Mode().Perm())
		}

		// Verify directory is empty.
		entries, err := os.ReadDir(targetDir)
		if err != nil {
			t.Fatalf("failed to read directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty directory, got %d entries", len(entries))
		}
	})

	t.Run("creates nested directories", func(t *testing.T) {
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "nested", "deep", "workspace")

		source := Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		}

		path, err := h.Prepare(context.Background(), source, targetDir)
		if err != nil {
			t.Fatalf("Prepare() failed: %v", err)
		}

		if path != targetDir {
			t.Errorf("returned path %q, want %q", path, targetDir)
		}

		// Verify nested directory exists.
		if _, err := os.Stat(targetDir); err != nil {
			t.Errorf("nested directory should exist: %v", err)
		}
	})

	t.Run("handles existing directory", func(t *testing.T) {
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "existing")

		// Pre-create the directory.
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			t.Fatalf("failed to pre-create directory: %v", err)
		}

		source := Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		}

		// MkdirAll should succeed on existing directory.
		path, err := h.Prepare(context.Background(), source, targetDir)
		if err != nil {
			t.Fatalf("Prepare() should succeed on existing directory: %v", err)
		}

		if path != targetDir {
			t.Errorf("returned path %q, want %q", path, targetDir)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		parentDir := t.TempDir()
		targetDir := filepath.Join(parentDir, "workspace-cancel")

		source := Source{
			Type:     SourceTypeEmptyDir,
			EmptyDir: EmptyDirSource{},
		}

		// Create a canceled context.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := h.Prepare(ctx, source, targetDir)
		if err == nil {
			t.Fatal("expected error from canceled context, got nil")
		}

		// Should return context cancellation error.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})
}
