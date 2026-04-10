// Package meta provides metadata storage for OAR agent and workspace records.
// It uses bbolt (pure Go embedded key-value store) for persistence.
package meta

import (
	"fmt"
	"log/slog"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket name constants for bbolt key hierarchy.
//
// Layout:
//
//	v1/
//	  workspaces/{name}              → Workspace JSON blob
//	  agents/{workspace}/{name}      → AgentRun JSON blob (nested buckets)
//	  agentruns/{workspace}/{name}   → (alias path; actual bucket name is "agentruns")
//	  agents/{name}                  → AgentTemplate JSON blob
var (
	bucketV1             = []byte("v1")
	bucketWorkspaces     = []byte("workspaces")
	bucketAgentRuns      = []byte("agentruns")
	bucketAgentTemplates = []byte("agents")
)

// Store is the bbolt-backed metadata store.
// It exposes typed CRUD operations for AgentRun, AgentTemplate, and Workspace objects.
// All writes use bbolt.Update transactions; all reads use bbolt.View transactions.
type Store struct {
	db     *bolt.DB
	logger *slog.Logger

	// Path is the filesystem path to the bbolt database file.
	Path string
}

// NewStore opens (or creates) a bbolt database at path and initializes the
// bucket hierarchy. It returns an error if the file cannot be opened within
// the 5-second lock timeout.
func NewStore(path string) (*Store, error) {
	logger := slog.Default().With("component", "meta.store", "path", path)
	logger.Info("opening metadata store")

	db, err := bolt.Open(path, 0o600, &bolt.Options{
		Timeout: 5 * time.Second,
	})
	if err != nil {
		logger.Error("failed to open bbolt database", "error", err)
		return nil, fmt.Errorf("meta: failed to open database at %s: %w", path, err)
	}

	s := &Store{
		db:     db,
		logger: logger,
		Path:   path,
	}

	if err := s.initBuckets(); err != nil {
		_ = db.Close()
		logger.Error("failed to initialize buckets", "error", err)
		return nil, fmt.Errorf("meta: failed to initialize buckets: %w", err)
	}

	logger.Info("metadata store opened")
	return s, nil
}

// initBuckets ensures all required top-level buckets exist.
// Runs in a single Update transaction at open time.
func (s *Store) initBuckets() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		v1, err := tx.CreateBucketIfNotExists(bucketV1)
		if err != nil {
			return fmt.Errorf("create v1 bucket: %w", err)
		}
		if _, err := v1.CreateBucketIfNotExists(bucketWorkspaces); err != nil {
			return fmt.Errorf("create v1/workspaces bucket: %w", err)
		}
		if _, err := v1.CreateBucketIfNotExists(bucketAgentRuns); err != nil {
			return fmt.Errorf("create v1/agentruns bucket: %w", err)
		}
		if _, err := v1.CreateBucketIfNotExists(bucketAgentTemplates); err != nil {
			return fmt.Errorf("create v1/agents bucket: %w", err)
		}
		return nil
	})
}

// Close closes the bbolt database and releases the file lock.
func (s *Store) Close() error {
	s.logger.Info("closing metadata store")
	if err := s.db.Close(); err != nil {
		s.logger.Error("failed to close database", "error", err)
		return fmt.Errorf("meta: failed to close database: %w", err)
	}
	s.logger.Info("metadata store closed")
	return nil
}

// workspacesBucket returns the v1/workspaces bucket from the given transaction.
func workspacesBucket(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket(bucketV1).Bucket(bucketWorkspaces)
}

// agentRunsBucket returns the v1/agentruns bucket from the given transaction.
func agentRunsBucket(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket(bucketV1).Bucket(bucketAgentRuns)
}

// workspaceBucket returns (or creates) the per-workspace sub-bucket under
// v1/agentruns/{workspace}. Must be called from an Update transaction.
func workspaceBucket(tx *bolt.Tx, workspace string) (*bolt.Bucket, error) {
	runs := agentRunsBucket(tx)
	if runs == nil {
		return nil, fmt.Errorf("v1/agentruns bucket not found")
	}
	return runs.CreateBucketIfNotExists([]byte(workspace))
}

// workspaceBucketRO returns the per-workspace sub-bucket under
// v1/agentruns/{workspace} for read-only access.
// Returns nil (not an error) if the workspace sub-bucket does not exist.
func workspaceBucketRO(tx *bolt.Tx, workspace string) *bolt.Bucket {
	runs := agentRunsBucket(tx)
	if runs == nil {
		return nil
	}
	return runs.Bucket([]byte(workspace))
}
