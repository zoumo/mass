// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkspaceCRUD(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Test CreateWorkspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Source: json.RawMessage(`{"type":"git","url":"https://github.com/test/repo.git"}`),
		Status: WorkspaceStatusActive,
	}

	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Test GetWorkspace.
	retrieved, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetWorkspace returned nil")
	}

	// Verify fields.
	if retrieved.ID != workspace.ID {
		t.Errorf("ID mismatch: got %s, want %s", retrieved.ID, workspace.ID)
	}
	if retrieved.Name != workspace.Name {
		t.Errorf("Name mismatch: got %s, want %s", retrieved.Name, workspace.Name)
	}
	if retrieved.Path != workspace.Path {
		t.Errorf("Path mismatch: got %s, want %s", retrieved.Path, workspace.Path)
	}
	if retrieved.Status != workspace.Status {
		t.Errorf("Status mismatch: got %s, want %s", retrieved.Status, workspace.Status)
	}
	if string(retrieved.Source) != string(workspace.Source) {
		t.Errorf("Source mismatch: got %s, want %s", retrieved.Source, workspace.Source)
	}
	if retrieved.RefCount != 0 {
		t.Errorf("RefCount should be 0, got %d", retrieved.RefCount)
	}

	// Test UpdateWorkspaceStatus.
	if err := store.UpdateWorkspaceStatus(ctx, workspace.ID, WorkspaceStatusInactive); err != nil {
		t.Fatalf("UpdateWorkspaceStatus failed: %v", err)
	}

	updated, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after update failed: %v", err)
	}
	if updated.Status != WorkspaceStatusInactive {
		t.Errorf("Status after update mismatch: got %s, want inactive", updated.Status)
	}

	// Test DeleteWorkspace.
	refsDeleted, err := store.DeleteWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("DeleteWorkspace failed: %v", err)
	}
	if refsDeleted != 0 {
		t.Errorf("Expected 0 refs deleted, got %d", refsDeleted)
	}

	// Verify deleted.
	deleted, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after delete failed: %v", err)
	}
	if deleted != nil {
		t.Error("Workspace should be deleted but still exists")
	}
}

func TestWorkspaceRefCounting(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Verify initial ref_count is 0.
	initial, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace failed: %v", err)
	}
	if initial.RefCount != 0 {
		t.Errorf("Initial RefCount should be 0, got %d", initial.RefCount)
	}

	// Create sessions first (required for FK constraint on workspace_refs.session_id).
	session1 := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        SessionStateRunning,
	}
	session2 := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        SessionStateRunning,
	}
	if err := store.CreateSession(ctx, session1); err != nil {
		t.Fatalf("CreateSession session1 failed: %v", err)
	}
	if err := store.CreateSession(ctx, session2); err != nil {
		t.Fatalf("CreateSession session2 failed: %v", err)
	}

	// Test AcquireWorkspace (session 1).
	if err := store.AcquireWorkspace(ctx, workspace.ID, session1.ID); err != nil {
		t.Fatalf("AcquireWorkspace session1 failed: %v", err)
	}

	after1, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after acquire1 failed: %v", err)
	}
	if after1.RefCount != 1 {
		t.Errorf("RefCount after first acquire should be 1, got %d", after1.RefCount)
	}

	// Test AcquireWorkspace (session 2).
	if err := store.AcquireWorkspace(ctx, workspace.ID, session2.ID); err != nil {
		t.Fatalf("AcquireWorkspace session2 failed: %v", err)
	}

	after2, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after acquire2 failed: %v", err)
	}
	if after2.RefCount != 2 {
		t.Errorf("RefCount after second acquire should be 2, got %d", after2.RefCount)
	}

	// Test double acquire (should fail).
	err = store.AcquireWorkspace(ctx, workspace.ID, session1.ID)
	if err == nil {
		t.Error("Double acquire should fail")
	}
	if !containsIgnoreCase(err.Error(), "already acquired") {
		t.Errorf("Expected 'already acquired' error, got: %v", err)
	}

	// Test ReleaseWorkspace (session 1).
	newCount, err := store.ReleaseWorkspace(ctx, workspace.ID, session1.ID)
	if err != nil {
		t.Fatalf("ReleaseWorkspace session1 failed: %v", err)
	}
	if newCount != 1 {
		t.Errorf("RefCount after release should be 1, got %d", newCount)
	}

	// Test ReleaseWorkspace (session 2).
	newCount, err = store.ReleaseWorkspace(ctx, workspace.ID, session2.ID)
	if err != nil {
		t.Fatalf("ReleaseWorkspace session2 failed: %v", err)
	}
	if newCount != 0 {
		t.Errorf("RefCount after second release should be 0, got %d", newCount)
	}

	// Test release already released (should fail).
	_, err = store.ReleaseWorkspace(ctx, workspace.ID, session1.ID)
	if err == nil {
		t.Error("Release already released should fail")
	}
	if !containsIgnoreCase(err.Error(), "was not acquired") {
		t.Errorf("Expected 'was not acquired' error, got: %v", err)
	}

	// Verify workspace cannot be deleted while sessions reference it (FK RESTRICT).
	_, err = store.DeleteWorkspace(ctx, workspace.ID)
	if err == nil {
		t.Error("DeleteWorkspace should fail while sessions reference the workspace")
	}

	// Delete sessions first (required to delete workspace due to FK RESTRICT).
	if err := store.DeleteSession(ctx, session1.ID); err != nil {
		t.Fatalf("DeleteSession session1 failed: %v", err)
	}
	if err := store.DeleteSession(ctx, session2.ID); err != nil {
		t.Fatalf("DeleteSession session2 failed: %v", err)
	}

	// Verify workspace can be deleted now (no refs, no sessions).
	refsDeleted, err := store.DeleteWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("DeleteWorkspace after releases failed: %v", err)
	}
	if refsDeleted != 0 {
		t.Errorf("Expected 0 refs deleted, got %d", refsDeleted)
	}
}

func TestWorkspaceCannotDeleteWithRefs(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Create session first (required for FK constraint).
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        SessionStateRunning,
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := store.AcquireWorkspace(ctx, workspace.ID, session.ID); err != nil {
		t.Fatalf("AcquireWorkspace failed: %v", err)
	}

	// Verify ref_count > 0.
	withRefs, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace failed: %v", err)
	}
	if withRefs.RefCount != 1 {
		t.Errorf("RefCount should be 1, got %d", withRefs.RefCount)
	}

	// Try to delete workspace with refs (should fail).
	_, err = store.DeleteWorkspace(ctx, workspace.ID)
	if err == nil {
		t.Error("DeleteWorkspace should fail with refs")
	}
	if !containsIgnoreCase(err.Error(), "references") {
		t.Errorf("Expected 'references' error, got: %v", err)
	}

	// Verify workspace still exists.
	stillExists, err := store.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after failed delete failed: %v", err)
	}
	if stillExists == nil {
		t.Error("Workspace should still exist")
	}
}

func TestAcquireNonActiveWorkspace(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create inactive workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "inactive-workspace",
		Path:   "/tmp/inactive-workspace",
		Status: WorkspaceStatusInactive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	sessionID := uuid.New().String()

	// Try to acquire inactive workspace (should fail).
	err := store.AcquireWorkspace(ctx, workspace.ID, sessionID)
	if err == nil {
		t.Error("AcquireWorkspace should fail for inactive workspace")
	}
	if !containsIgnoreCase(err.Error(), "not active") {
		t.Errorf("Expected 'not active' error, got: %v", err)
	}
}

func TestAcquireNonExistentWorkspace(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	sessionID := uuid.New().String()

	// Try to acquire non-existent workspace.
	err := store.AcquireWorkspace(ctx, uuid.New().String(), sessionID)
	if err == nil {
		t.Error("AcquireWorkspace should fail for non-existent workspace")
	}
	if !containsIgnoreCase(err.Error(), "does not exist") {
		t.Errorf("Expected 'does not exist' error, got: %v", err)
	}
}

func TestListWorkspacesFiltering(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create workspaces with different statuses.
	activeWorkspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "active-ws",
		Path:   "/tmp/active",
		Status: WorkspaceStatusActive,
	}
	inactiveWorkspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "inactive-ws",
		Path:   "/tmp/inactive",
		Status: WorkspaceStatusInactive,
	}

	if err := store.CreateWorkspace(ctx, activeWorkspace); err != nil {
		t.Fatalf("CreateWorkspace active failed: %v", err)
	}
	if err := store.CreateWorkspace(ctx, inactiveWorkspace); err != nil {
		t.Fatalf("CreateWorkspace inactive failed: %v", err)
	}

	// Acquire the active workspace to test HasRefs filter.
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  activeWorkspace.ID,
		State:        SessionStateRunning,
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := store.AcquireWorkspace(ctx, activeWorkspace.ID, session.ID); err != nil {
		t.Fatalf("AcquireWorkspace failed: %v", err)
	}

	// Test filter by status.
	activeOnly, err := store.ListWorkspaces(ctx, &WorkspaceFilter{Status: WorkspaceStatusActive})
	if err != nil {
		t.Fatalf("ListWorkspaces by status failed: %v", err)
	}
	if len(activeOnly) != 1 {
		t.Errorf("Expected 1 active workspace, got %d", len(activeOnly))
	}
	if len(activeOnly) > 0 && activeOnly[0].ID != activeWorkspace.ID {
		t.Errorf("Active workspace ID mismatch")
	}

	// Test filter by name.
	byName, err := store.ListWorkspaces(ctx, &WorkspaceFilter{Name: "inactive-ws"})
	if err != nil {
		t.Fatalf("ListWorkspaces by name failed: %v", err)
	}
	if len(byName) != 1 {
		t.Errorf("Expected 1 workspace with name 'inactive-ws', got %d", len(byName))
	}

	// Test filter HasRefs=true.
	withRefs, err := store.ListWorkspaces(ctx, &WorkspaceFilter{HasRefs: boolPtr(true)})
	if err != nil {
		t.Fatalf("ListWorkspaces HasRefs=true failed: %v", err)
	}
	if len(withRefs) != 1 {
		t.Errorf("Expected 1 workspace with refs, got %d", len(withRefs))
	}
	if len(withRefs) > 0 && withRefs[0].ID != activeWorkspace.ID {
		t.Errorf("Workspace with refs ID mismatch")
	}

	// Test filter HasRefs=false.
	noRefs, err := store.ListWorkspaces(ctx, &WorkspaceFilter{HasRefs: boolPtr(false)})
	if err != nil {
		t.Fatalf("ListWorkspaces HasRefs=false failed: %v", err)
	}
	if len(noRefs) != 1 {
		t.Errorf("Expected 1 workspace without refs, got %d", len(noRefs))
	}

	// Test no filter (all workspaces).
	all, err := store.ListWorkspaces(ctx, nil)
	if err != nil {
		t.Fatalf("ListWorkspaces all failed: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("Expected 2 total workspaces, got %d", len(all))
	}
}

func TestWorkspaceTransactionRollback(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Start transaction.
	tx, err := store.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	// Create workspace in transaction.
	workspaceID := uuid.New().String()
	query := `
		INSERT INTO workspaces (id, name, path, source, status, ref_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.ExecContext(ctx, query, workspaceID, "tx-workspace", "/tmp/tx", "{}", WorkspaceStatusActive, 0, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Insert in transaction failed: %v", err)
	}

	// Rollback transaction.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify workspace was not created.
	retrieved, err := store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace after rollback failed: %v", err)
	}
	if retrieved != nil {
		t.Error("Workspace should not exist after rollback")
	}
}

func TestWorkspaceDuplicateID(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Try to create workspace with same ID.
	duplicate := &Workspace{
		ID:     workspace.ID,
		Name:   "duplicate-workspace",
		Path:   "/tmp/duplicate",
		Status: WorkspaceStatusActive,
	}
	err := store.CreateWorkspace(ctx, duplicate)
	if err == nil {
		t.Error("CreateWorkspace should fail with duplicate ID")
	}
	if !containsIgnoreCase(err.Error(), "already exists") {
		t.Errorf("Expected 'already exists' error, got: %v", err)
	}
}

func TestWorkspaceUpdateNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to update non-existent workspace.
	err := store.UpdateWorkspaceStatus(ctx, uuid.New().String(), WorkspaceStatusInactive)
	if err == nil {
		t.Error("UpdateWorkspaceStatus should fail for non-existent workspace")
	}
}

func TestWorkspaceDeleteNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to delete non-existent workspace.
	_, err := store.DeleteWorkspace(ctx, uuid.New().String())
	if err == nil {
		t.Error("DeleteWorkspace should fail for non-existent workspace")
	}
}
