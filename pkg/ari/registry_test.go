// Package ari tests the Registry rebuild-from-DB functionality.
package ari

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	apiari "github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/pkg/store"
	"github.com/zoumo/oar/pkg/workspace"
)

// TestRegistryRebuildFromDB verifies that RebuildFromDB loads ready workspaces
// from the metadata store into the registry with correct fields.
func TestRegistryRebuildFromDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := store.NewStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Workspace 1: git source, phase=ready.
	src1JSON, _ := json.Marshal(workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: "https://github.com/example/repo.git", Ref: "main", Depth: 1},
	})
	if err := store.CreateWorkspace(ctx, &apiari.Workspace{
		Metadata: apiari.ObjectMeta{Name: "git-workspace"},
		Spec:     apiari.WorkspaceSpec{Source: src1JSON},
		Status:   apiari.WorkspaceStatus{Phase: apiari.WorkspacePhaseReady, Path: "/var/workspaces/git-workspace"},
	}); err != nil {
		t.Fatalf("CreateWorkspace git-workspace: %v", err)
	}

	// Workspace 2: emptyDir source, phase=ready.
	src2JSON, _ := json.Marshal(workspace.Source{Type: workspace.SourceTypeEmptyDir})
	if err := store.CreateWorkspace(ctx, &apiari.Workspace{
		Metadata: apiari.ObjectMeta{Name: "empty-workspace"},
		Spec:     apiari.WorkspaceSpec{Source: src2JSON},
		Status:   apiari.WorkspaceStatus{Phase: apiari.WorkspacePhaseReady, Path: "/var/workspaces/empty-workspace"},
	}); err != nil {
		t.Fatalf("CreateWorkspace empty-workspace: %v", err)
	}

	// Workspace 3: phase=pending — should NOT appear after rebuild.
	if err := store.CreateWorkspace(ctx, &apiari.Workspace{
		Metadata: apiari.ObjectMeta{Name: "pending-workspace"},
		Status:   apiari.WorkspaceStatus{Phase: apiari.WorkspacePhasePending},
	}); err != nil {
		t.Fatalf("CreateWorkspace pending-workspace: %v", err)
	}

	// Rebuild registry.
	registry := NewRegistry()
	if err := registry.RebuildFromDB(store); err != nil {
		t.Fatalf("RebuildFromDB failed: %v", err)
	}

	// Verify results.
	list := registry.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 workspaces in registry, got %d", len(list))
	}

	// Check workspace 1.
	m1 := registry.Get("git-workspace")
	if m1 == nil {
		t.Fatal("git-workspace not found in registry")
	}
	if m1.Name != "git-workspace" {
		t.Errorf("Name: got %q, want git-workspace", m1.Name)
	}
	if m1.Path != "/var/workspaces/git-workspace" {
		t.Errorf("Path: got %q, want /var/workspaces/git-workspace", m1.Path)
	}
	if m1.Status != "ready" {
		t.Errorf("Status: got %q, want ready", m1.Status)
	}
	if m1.RefCount != 0 {
		t.Errorf("RefCount: got %d, want 0 (not tracked in DB)", m1.RefCount)
	}
	if len(m1.Refs) != 0 {
		t.Errorf("Refs: got %d entries, want 0", len(m1.Refs))
	}
	if m1.Spec.Source.Type != workspace.SourceTypeGit {
		t.Errorf("Source.Type: got %q, want git", m1.Spec.Source.Type)
	}
	if m1.Spec.Source.Git.URL != "https://github.com/example/repo.git" {
		t.Errorf("Source.Git.URL: got %q, want example URL", m1.Spec.Source.Git.URL)
	}
	if m1.Spec.Metadata.Name != "git-workspace" {
		t.Errorf("Spec.Metadata.Name: got %q, want git-workspace", m1.Spec.Metadata.Name)
	}

	// Check workspace 2.
	m2 := registry.Get("empty-workspace")
	if m2 == nil {
		t.Fatal("empty-workspace not found in registry")
	}
	if m2.Spec.Source.Type != workspace.SourceTypeEmptyDir {
		t.Errorf("empty-workspace Source.Type: got %q, want emptyDir", m2.Spec.Source.Type)
	}

	// Pending workspace must NOT be in registry.
	if m3 := registry.Get("pending-workspace"); m3 != nil {
		t.Error("pending-workspace should NOT be in registry after RebuildFromDB")
	}
}

// TestRegistryAddGetRemove verifies in-memory Add/Get/Remove.
func TestRegistryAddGetRemove(t *testing.T) {
	r := NewRegistry()
	spec := workspace.WorkspaceSpec{Metadata: workspace.WorkspaceMetadata{Name: "my-ws"}}
	r.Add("my-ws", "my-ws", "/tmp/my-ws", spec)

	m := r.Get("my-ws")
	if m == nil {
		t.Fatal("expected to find my-ws in registry")
	}
	if m.Name != "my-ws" {
		t.Errorf("Name: got %q, want my-ws", m.Name)
	}
	if m.Path != "/tmp/my-ws" {
		t.Errorf("Path: got %q, want /tmp/my-ws", m.Path)
	}
	if m.Status != "ready" {
		t.Errorf("Status: got %q, want ready", m.Status)
	}

	r.Remove("my-ws")
	if got := r.Get("my-ws"); got != nil {
		t.Error("expected nil after Remove")
	}
}

// TestRegistryAcquireRelease verifies reference counting.
func TestRegistryAcquireRelease(t *testing.T) {
	r := NewRegistry()
	r.Add("ws1", "ws1", "/tmp/ws1", workspace.WorkspaceSpec{})

	r.Acquire("ws1", "workspace1/agent-a")
	r.Acquire("ws1", "workspace1/agent-b")

	m := r.Get("ws1")
	if m == nil {
		t.Fatal("ws1 not found")
	}
	if m.RefCount != 2 {
		t.Errorf("RefCount: got %d, want 2", m.RefCount)
	}
	if len(m.Refs) != 2 {
		t.Errorf("Refs: got %d, want 2", len(m.Refs))
	}

	if count := r.Release("ws1", "workspace1/agent-a"); count != 1 {
		t.Errorf("Release: got %d, want 1", count)
	}
	if count := r.Release("ws1", "workspace1/agent-b"); count != 0 {
		t.Errorf("Release: got %d, want 0", count)
	}
}
