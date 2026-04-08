// Package ari tests the Registry rebuild-from-DB functionality.
package ari

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// TestRegistryRebuildFromDB verifies that RebuildFromDB loads workspaces
// from the metadata store into the registry with correct RefCount, Refs,
// and deserialized Source specs.
func TestRegistryRebuildFromDB(t *testing.T) {
	// Create an in-memory store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := meta.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// --- Seed DB with two workspaces ---

	// Workspace 1: git source, 2 refs.
	src1 := workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: "https://github.com/example/repo.git", Ref: "main", Depth: 1},
	}
	src1JSON, _ := json.Marshal(src1)

	ws1 := &meta.Workspace{
		ID:     "ws-001",
		Name:   "git-workspace",
		Path:   "/var/workspaces/ws-001",
		Source: src1JSON,
		Status: meta.WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, ws1); err != nil {
		t.Fatalf("CreateWorkspace ws-001: %v", err)
	}

	// Create sessions that reference ws-001.
	sess1 := &meta.Session{ID: "sess-aaa", RuntimeClass: "default", WorkspaceID: "ws-001", State: meta.SessionStateRunning}
	sess2 := &meta.Session{ID: "sess-bbb", RuntimeClass: "default", WorkspaceID: "ws-001", State: meta.SessionStateRunning}
	if err := store.CreateSession(ctx, sess1); err != nil {
		t.Fatalf("CreateSession sess-aaa: %v", err)
	}
	if err := store.CreateSession(ctx, sess2); err != nil {
		t.Fatalf("CreateSession sess-bbb: %v", err)
	}
	if err := store.AcquireWorkspace(ctx, "ws-001", "sess-aaa"); err != nil {
		t.Fatalf("AcquireWorkspace sess-aaa: %v", err)
	}
	if err := store.AcquireWorkspace(ctx, "ws-001", "sess-bbb"); err != nil {
		t.Fatalf("AcquireWorkspace sess-bbb: %v", err)
	}

	// Workspace 2: emptyDir source, 0 refs.
	src2 := workspace.Source{Type: workspace.SourceTypeEmptyDir}
	src2JSON, _ := json.Marshal(src2)

	ws2 := &meta.Workspace{
		ID:     "ws-002",
		Name:   "empty-workspace",
		Path:   "/var/workspaces/ws-002",
		Source: src2JSON,
		Status: meta.WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, ws2); err != nil {
		t.Fatalf("CreateWorkspace ws-002: %v", err)
	}

	// Workspace 3: inactive — should NOT appear after rebuild.
	ws3 := &meta.Workspace{
		ID:     "ws-003",
		Name:   "inactive-workspace",
		Path:   "/var/workspaces/ws-003",
		Source: json.RawMessage(`{"type":"local","path":"/tmp/local"}`),
		Status: meta.WorkspaceStatusInactive,
	}
	if err := store.CreateWorkspace(ctx, ws3); err != nil {
		t.Fatalf("CreateWorkspace ws-003: %v", err)
	}

	// --- Rebuild registry ---
	registry := NewRegistry()
	if err := registry.RebuildFromDB(store); err != nil {
		t.Fatalf("RebuildFromDB failed: %v", err)
	}

	// --- Verify results ---
	list := registry.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 workspaces in registry, got %d", len(list))
	}

	// Check workspace 1.
	m1 := registry.Get("ws-001")
	if m1 == nil {
		t.Fatal("ws-001 not found in registry")
	}
	if m1.Name != "git-workspace" {
		t.Errorf("ws-001 Name: got %q, want %q", m1.Name, "git-workspace")
	}
	if m1.Path != "/var/workspaces/ws-001" {
		t.Errorf("ws-001 Path: got %q, want %q", m1.Path, "/var/workspaces/ws-001")
	}
	if m1.RefCount != 2 {
		t.Errorf("ws-001 RefCount: got %d, want 2", m1.RefCount)
	}
	if len(m1.Refs) != 2 {
		t.Errorf("ws-001 Refs: got %d entries, want 2", len(m1.Refs))
	}
	if m1.Status != "ready" {
		t.Errorf("ws-001 Status: got %q, want %q", m1.Status, "ready")
	}
	// Verify Source was deserialized correctly.
	if m1.Spec.Source.Type != workspace.SourceTypeGit {
		t.Errorf("ws-001 Source.Type: got %q, want %q", m1.Spec.Source.Type, workspace.SourceTypeGit)
	}
	if m1.Spec.Source.Git.URL != "https://github.com/example/repo.git" {
		t.Errorf("ws-001 Source.Git.URL: got %q, want %q", m1.Spec.Source.Git.URL, "https://github.com/example/repo.git")
	}
	if m1.Spec.Source.Git.Ref != "main" {
		t.Errorf("ws-001 Source.Git.Ref: got %q, want %q", m1.Spec.Source.Git.Ref, "main")
	}
	if m1.Spec.Source.Git.Depth != 1 {
		t.Errorf("ws-001 Source.Git.Depth: got %d, want 1", m1.Spec.Source.Git.Depth)
	}
	if m1.Spec.Metadata.Name != "git-workspace" {
		t.Errorf("ws-001 Spec.Metadata.Name: got %q, want %q", m1.Spec.Metadata.Name, "git-workspace")
	}

	// Check workspace 2.
	m2 := registry.Get("ws-002")
	if m2 == nil {
		t.Fatal("ws-002 not found in registry")
	}
	if m2.Name != "empty-workspace" {
		t.Errorf("ws-002 Name: got %q, want %q", m2.Name, "empty-workspace")
	}
	if m2.RefCount != 0 {
		t.Errorf("ws-002 RefCount: got %d, want 0", m2.RefCount)
	}
	if len(m2.Refs) != 0 {
		t.Errorf("ws-002 Refs: got %d entries, want 0", len(m2.Refs))
	}
	if m2.Spec.Source.Type != workspace.SourceTypeEmptyDir {
		t.Errorf("ws-002 Source.Type: got %q, want %q", m2.Spec.Source.Type, workspace.SourceTypeEmptyDir)
	}

	// Check workspace 3 is NOT in registry (inactive).
	if m3 := registry.Get("ws-003"); m3 != nil {
		t.Error("ws-003 (inactive) should NOT be in registry after RebuildFromDB")
	}
}
