// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRoomCRUD(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Test CreateRoom.
	room := &Room{
		Name:             "test-room",
		Labels:           map[string]string{"env": "test", "team": "dev"},
		CommunicationMode: CommunicationModeMesh,
	}

	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Test GetRoom.
	retrieved, err := store.GetRoom(ctx, room.Name)
	if err != nil {
		t.Fatalf("GetRoom failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetRoom returned nil")
	}

	// Verify fields.
	if retrieved.Name != room.Name {
		t.Errorf("Name mismatch: got %s, want %s", retrieved.Name, room.Name)
	}
	if retrieved.CommunicationMode != room.CommunicationMode {
		t.Errorf("CommunicationMode mismatch: got %s, want %s", retrieved.CommunicationMode, room.CommunicationMode)
	}
	if retrieved.Labels["env"] != "test" {
		t.Errorf("Labels[env] mismatch: got %s, want test", retrieved.Labels["env"])
	}
	if retrieved.Labels["team"] != "dev" {
		t.Errorf("Labels[team] mismatch: got %s, want dev", retrieved.Labels["team"])
	}

	// Test DeleteRoom.
	if err := store.DeleteRoom(ctx, room.Name); err != nil {
		t.Fatalf("DeleteRoom failed: %v", err)
	}

	// Verify deleted.
	deleted, err := store.GetRoom(ctx, room.Name)
	if err != nil {
		t.Fatalf("GetRoom after delete failed: %v", err)
	}
	if deleted != nil {
		t.Error("Room should be deleted but still exists")
	}
}

func TestRoomCommunicationModes(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Test different communication modes.
	modes := []CommunicationMode{
		CommunicationModeMesh,
		CommunicationModeStar,
		CommunicationModeIsolated,
	}

	for i, mode := range modes {
		room := &Room{
			Name:             "room-" + string(mode),
			CommunicationMode: mode,
		}

		if err := store.CreateRoom(ctx, room); err != nil {
			t.Fatalf("CreateRoom mode %s failed: %v", mode, err)
		}

		retrieved, err := store.GetRoom(ctx, room.Name)
		if err != nil {
			t.Fatalf("GetRoom mode %s failed: %v", mode, err)
		}
		if retrieved.CommunicationMode != mode {
			t.Errorf("Mode %d mismatch: got %s, want %s", i, retrieved.CommunicationMode, mode)
		}
	}
}

func TestRoomDuplicateName(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	room := &Room{
		Name:             "duplicate-test",
		CommunicationMode: CommunicationModeMesh,
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Try to create room with same name.
	duplicate := &Room{
		Name:             "duplicate-test",
		CommunicationMode: CommunicationModeStar,
	}
	err := store.CreateRoom(ctx, duplicate)
	if err == nil {
		t.Error("CreateRoom should fail with duplicate name")
	}
	if !containsIgnoreCase(err.Error(), "already exists") {
		t.Errorf("Expected 'already exists' error, got: %v", err)
	}
}

func TestListRoomsFiltering(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create rooms with different communication modes.
	rooms := []*Room{
		{Name: "mesh-room", CommunicationMode: CommunicationModeMesh},
		{Name: "star-room", CommunicationMode: CommunicationModeStar},
		{Name: "isolated-room", CommunicationMode: CommunicationModeIsolated},
		{Name: "another-mesh", CommunicationMode: CommunicationModeMesh},
	}

	for _, r := range rooms {
		if err := store.CreateRoom(ctx, r); err != nil {
			t.Fatalf("CreateRoom %s failed: %v", r.Name, err)
		}
	}

	// Test filter by communication mode.
	meshOnly, err := store.ListRooms(ctx, &RoomFilter{CommunicationMode: CommunicationModeMesh})
	if err != nil {
		t.Fatalf("ListRooms by mode failed: %v", err)
	}
	if len(meshOnly) != 2 {
		t.Errorf("Expected 2 mesh rooms, got %d", len(meshOnly))
	}

	starOnly, err := store.ListRooms(ctx, &RoomFilter{CommunicationMode: CommunicationModeStar})
	if err != nil {
		t.Fatalf("ListRooms direct mode failed: %v", err)
	}
	if len(starOnly) != 1 {
		t.Errorf("Expected 1 star room, got %d", len(starOnly))
	}

	// Test no filter (all rooms).
	all, err := store.ListRooms(ctx, nil)
	if err != nil {
		t.Fatalf("ListRooms all failed: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("Expected 4 total rooms, got %d", len(all))
	}
}

func TestRoomDeleteWithSessions(t *testing.T) {
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

	// Create room.
	room := &Room{
		Name:             "room-to-delete",
		CommunicationMode: CommunicationModeMesh,
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Create session associated with room.
	session := &Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		Room:         room.Name,
		RoomAgent:    "agent-1",
		State:        SessionStateRunning,
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Delete room (sessions should have room set to NULL via ON DELETE SET NULL).
	if err := store.DeleteRoom(ctx, room.Name); err != nil {
		t.Fatalf("DeleteRoom failed: %v", err)
	}

	// Verify room deleted.
	deletedRoom, err := store.GetRoom(ctx, room.Name)
	if err != nil {
		t.Fatalf("GetRoom after delete failed: %v", err)
	}
	if deletedRoom != nil {
		t.Error("Room should be deleted")
	}

	// Verify session still exists but room is now empty.
	retrievedSession, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after room delete failed: %v", err)
	}
	if retrievedSession == nil {
		t.Fatal("Session should still exist")
	}
	// Note: ON DELETE SET NULL sets room to NULL, which we store as empty string.
	if retrievedSession.Room != "" {
		t.Errorf("Session room should be empty after room delete, got: %s", retrievedSession.Room)
	}
}

func TestRoomTransactionRollback(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Start transaction.
	tx, err := store.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	// Create room in transaction.
	roomName := "tx-room"
	query := `
		INSERT INTO rooms (name, labels, communication_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err = tx.ExecContext(ctx, query, roomName, "{}", CommunicationModeMesh, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Insert in transaction failed: %v", err)
	}

	// Rollback transaction.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify room was not created.
	retrieved, err := store.GetRoom(ctx, roomName)
	if err != nil {
		t.Fatalf("GetRoom after rollback failed: %v", err)
	}
	if retrieved != nil {
		t.Error("Room should not exist after rollback")
	}
}

func TestRoomDeleteNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Try to delete non-existent room.
	err := store.DeleteRoom(ctx, "non-existent-room")
	if err == nil {
		t.Error("DeleteRoom should fail for non-existent room")
	}
	if !containsIgnoreCase(err.Error(), "does not exist") {
		t.Errorf("Expected 'does not exist' error, got: %v", err)
	}
}

func TestRoomGetNonExistent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Get non-existent room should return nil.
	retrieved, err := store.GetRoom(ctx, "non-existent-room")
	if err != nil {
		t.Fatalf("GetRoom failed: %v", err)
	}
	if retrieved != nil {
		t.Error("GetRoom should return nil for non-existent room")
	}
}

func TestRoomEmptyLabels(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create room with no labels.
	room := &Room{
		Name:             "no-labels-room",
		CommunicationMode: CommunicationModeMesh,
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	retrieved, err := store.GetRoom(ctx, room.Name)
	if err != nil {
		t.Fatalf("GetRoom failed: %v", err)
	}
	if retrieved.Labels != nil && len(retrieved.Labels) != 0 {
		t.Errorf("Labels should be nil/empty, got: %v", retrieved.Labels)
	}
}