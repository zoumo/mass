// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSessionCRUD(t *testing.T) {
	// Create in-memory database.
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create prerequisite workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Test CreateSession.
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        SessionStateRunning,
		Labels:       map[string]string{"env": "test", "team": "dev"},
	}

	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Test GetSession.
	retrieved, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetSession returned nil")
	}

	// Verify fields.
	if retrieved.ID != session.ID {
		t.Errorf("ID mismatch: got %s, want %s", retrieved.ID, session.ID)
	}
	if retrieved.RuntimeClass != session.RuntimeClass {
		t.Errorf("RuntimeClass mismatch: got %s, want %s", retrieved.RuntimeClass, session.RuntimeClass)
	}
	if retrieved.WorkspaceID != session.WorkspaceID {
		t.Errorf("WorkspaceID mismatch: got %s, want %s", retrieved.WorkspaceID, session.WorkspaceID)
	}
	if retrieved.State != session.State {
		t.Errorf("State mismatch: got %s, want %s", retrieved.State, session.State)
	}
	if retrieved.Labels["env"] != "test" {
		t.Errorf("Labels[env] mismatch: got %s, want test", retrieved.Labels["env"])
	}

	// Test UpdateSession (state only).
	if err := store.UpdateSession(ctx, session.ID, SessionStateStopped, nil); err != nil {
		t.Fatalf("UpdateSession state failed: %v", err)
	}

	updated, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after update failed: %v", err)
	}
	if updated.State != SessionStateStopped {
		t.Errorf("State after update mismatch: got %s, want stopped", updated.State)
	}

	// Test UpdateSession (labels only).
	newLabels := map[string]string{"env": "prod", "version": "v2"}
	if err := store.UpdateSession(ctx, session.ID, "", newLabels); err != nil {
		t.Fatalf("UpdateSession labels failed: %v", err)
	}

	updated, err = store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after labels update failed: %v", err)
	}
	if updated.Labels["env"] != "prod" {
		t.Errorf("Labels[env] after update mismatch: got %s, want prod", updated.Labels["env"])
	}
	if updated.Labels["version"] != "v2" {
		t.Errorf("Labels[version] after update mismatch: got %s, want v2", updated.Labels["version"])
	}

	// Test DeleteSession.
	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify deleted.
	deleted, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after delete failed: %v", err)
	}
	if deleted != nil {
		t.Error("Session should be deleted but still exists")
	}
}

func TestSessionWithRoom(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create prerequisite workspace and room.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	room := &Room{
		Name:             "test-room",
		CommunicationMode: CommunicationModeBroadcast,
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Create session with room.
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		Room:         room.Name,
		RoomAgent:    "agent-1",
		State:        SessionStateRunning,
	}

	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession with room failed: %v", err)
	}

	// Verify room association.
	retrieved, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved.Room != room.Name {
		t.Errorf("Room mismatch: got %s, want %s", retrieved.Room, room.Name)
	}
	if retrieved.RoomAgent != "agent-1" {
		t.Errorf("RoomAgent mismatch: got %s, want agent-1", retrieved.RoomAgent)
	}
}

func TestSessionFKConstraint(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to create session without workspace.
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  uuid.New().String(), // Non-existent workspace.
		State:        SessionStateRunning,
	}

	err := store.CreateSession(ctx, session)
	if err == nil {
		t.Error("CreateSession should fail with non-existent workspace")
	}
	if !containsFKError(err.Error()) {
		t.Errorf("Expected FK constraint error, got: %v", err)
	}
}

func TestListSessionsFiltering(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create prerequisite workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Create prerequisite room.
	room := &Room{
		Name:             "room-with-sessions",
		CommunicationMode: CommunicationModeBroadcast,
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Create sessions with different states and room associations.
	sessions := []*Session{
		{
			ID:           uuid.New().String(),
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
			State:        SessionStateRunning,
			Room:         "",
		},
		{
			ID:           uuid.New().String(),
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
			State:        SessionStateRunning,
			Room:         room.Name,
		},
		{
			ID:           uuid.New().String(),
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
			State:        SessionStateStopped,
			Room:         "",
		},
		{
			ID:           uuid.New().String(),
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
			State:        SessionStatePausedWarm,
			Room:         room.Name,
		},
	}

	for _, s := range sessions {
		if err := store.CreateSession(ctx, s); err != nil {
			t.Fatalf("CreateSession failed: %v", err)
		}
	}

	// Test filter by state.
	runningSessions, err := store.ListSessions(ctx, &SessionFilter{State: SessionStateRunning})
	if err != nil {
		t.Fatalf("ListSessions by state failed: %v", err)
	}
	if len(runningSessions) != 2 {
		t.Errorf("Expected 2 running sessions, got %d", len(runningSessions))
	}

	// Test filter by workspace.
	wsSessions, err := store.ListSessions(ctx, &SessionFilter{WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatalf("ListSessions by workspace failed: %v", err)
	}
	if len(wsSessions) != 4 {
		t.Errorf("Expected 4 sessions for workspace, got %d", len(wsSessions))
	}

	// Test filter by room.
	roomSessions, err := store.ListSessions(ctx, &SessionFilter{Room: room.Name})
	if err != nil {
		t.Fatalf("ListSessions by room failed: %v", err)
	}
	if len(roomSessions) != 2 {
		t.Errorf("Expected 2 sessions in room, got %d", len(roomSessions))
	}

	// Test filter HasRoom=true.
	hasRoomSessions, err := store.ListSessions(ctx, &SessionFilter{HasRoom: boolPtr(true)})
	if err != nil {
		t.Fatalf("ListSessions HasRoom=true failed: %v", err)
	}
	if len(hasRoomSessions) != 2 {
		t.Errorf("Expected 2 sessions with room, got %d", len(hasRoomSessions))
	}

	// Test filter HasRoom=false.
	noRoomSessions, err := store.ListSessions(ctx, &SessionFilter{HasRoom: boolPtr(false)})
	if err != nil {
		t.Fatalf("ListSessions HasRoom=false failed: %v", err)
	}
	if len(noRoomSessions) != 2 {
		t.Errorf("Expected 2 sessions without room, got %d", len(noRoomSessions))
	}

	// Test no filter (all sessions).
	allSessions, err := store.ListSessions(ctx, nil)
	if err != nil {
		t.Fatalf("ListSessions all failed: %v", err)
	}
	if len(allSessions) != 4 {
		t.Errorf("Expected 4 total sessions, got %d", len(allSessions))
	}
}

func TestSessionTransactionRollback(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create prerequisite workspace.
	workspace := &Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Start transaction.
	tx, err := store.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	// Create session in transaction.
	sessionID := uuid.New().String()
	query := `
		INSERT INTO sessions (id, runtime_class, workspace_id, room, room_agent, labels, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.ExecContext(ctx, query, sessionID, "default", workspace.ID, nil, "", "{}", SessionStateRunning, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Insert in transaction failed: %v", err)
	}

	// Rollback transaction.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify session was not created.
	retrieved, err := store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession after rollback failed: %v", err)
	}
	if retrieved != nil {
		t.Error("Session should not exist after rollback")
	}
}

func TestSessionUpdateNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to update non-existent session.
	err := store.UpdateSession(ctx, uuid.New().String(), SessionStateStopped, nil)
	if err == nil {
		t.Error("UpdateSession should fail for non-existent session")
	}
}

func TestSessionDeleteNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to delete non-existent session.
	err := store.DeleteSession(ctx, uuid.New().String())
	if err == nil {
		t.Error("DeleteSession should fail for non-existent session")
	}
}

func containsFKError(msg string) bool {
	return containsIgnoreCase(msg, "FOREIGN KEY") ||
		containsIgnoreCase(msg, "foreign key") ||
		containsIgnoreCase(msg, "constraint")
}

func boolPtr(b bool) *bool {
	return &b
}