// Package meta provides metadata storage for OAR session/workspace/room records.
// It uses SQLite for persistence with transaction support.
package meta

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"strings"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

//go:embed schema.sql
var schemaFS embed.FS

// schemaSQL is the embedded schema SQL content.
var schemaSQL string

func init() {
	data, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(fmt.Sprintf("meta: failed to read embedded schema.sql: %v", err))
	}
	schemaSQL = string(data)
}

// Store wraps a SQL database connection for metadata storage.
// It provides CRUD operations for sessions, workspaces, and rooms
// with transaction support.
type Store struct {
	db *sql.DB

	// Path is the filesystem path to the SQLite database file.
	Path string

	// Logger is the structured logger for this store.
	Logger *slog.Logger
}

// NewStore creates a new metadata store at the given path.
// It opens the SQLite database with WAL journal mode and foreign keys enabled,
// creates the schema if it doesn't exist, and returns the Store.
//
// The connection string uses these parameters:
//   - _journal_mode=WAL: Write-Ahead Logging for better concurrency
//   - _foreign_keys=ON: Enforce foreign key constraints
//   - _busy_timeout=5000: Wait up to 5 seconds for locks
//
// Returns an error if the database cannot be opened or schema creation fails.
func NewStore(path string) (*Store, error) {
	logger := slog.Default().With("component", "meta.store", "path", path)

	logger.Info("opening metadata store")

	// Build connection string with SQLite parameters.
	// Format: file:path?_journal_mode=WAL&_foreign_keys=ON&_busy_timeout=5000
	dsn := buildDSN(path)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		logger.Error("failed to open database", "error", err)
		return nil, fmt.Errorf("meta: failed to open database at %s: %w", path, err)
	}

	// Verify connection is working.
	if err := db.Ping(); err != nil {
		db.Close()
		logger.Error("failed to ping database", "error", err)
		return nil, fmt.Errorf("meta: failed to ping database at %s: %w", path, err)
	}

	store := &Store{
		db:     db,
		Path:   path,
		Logger: logger,
	}

	// Initialize schema.
	if err := store.initSchema(); err != nil {
		store.Close()
		logger.Error("failed to initialize schema", "error", err)
		return nil, fmt.Errorf("meta: failed to initialize schema: %w", err)
	}

	logger.Info("metadata store initialized successfully")

	return store, nil
}

// buildDSN constructs a SQLite data source name with the required parameters.
func buildDSN(path string) string {
	// SQLite connection string format:
	// file:path?param1=value1&param2=value2
	params := []string{
		"_journal_mode=WAL",
		"_foreign_keys=ON",
		"_busy_timeout=5000",
	}
	return fmt.Sprintf("file:%s?%s", path, strings.Join(params, "&"))
}

// initSchema executes the embedded schema SQL to create tables.
// It runs each statement separately to handle the multi-statement schema file.
func (s *Store) initSchema() error {
	s.Logger.Info("initializing database schema")

	// Split schema into individual statements and execute each.
	// The schema contains multiple CREATE TABLE and CREATE INDEX statements,
	// plus triggers and an INSERT for schema_version.
	statements := splitStatements(schemaSQL)

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		if _, err := s.db.Exec(stmt); err != nil {
			// Some errors are acceptable (e.g., "table already exists" from CREATE IF NOT EXISTS).
			// But we should still log them for debugging.
			s.Logger.Debug("schema statement execution", "statement", truncate(stmt, 100), "error", err)

			// Check if this is a benign error we can ignore.
			if isBenignSchemaError(err) {
				continue
			}

			return fmt.Errorf("meta: schema initialization failed: %w (statement: %s)", err, truncate(stmt, 50))
		}
	}

	s.Logger.Info("database schema initialized")

	return nil
}

// splitStatements splits a multi-statement SQL file into individual statements.
// It handles comments, BEGIN...END blocks (for triggers), and statement boundaries.
func splitStatements(sqlText string) []string {
	var statements []string
	var current strings.Builder
	inBeginBlock := false

	for _, line := range strings.Split(sqlText, "\n") {
		trimmed := strings.TrimSpace(line)

		// Skip comment lines.
		if strings.HasPrefix(trimmed, "--") {
			continue
		}

		current.WriteString(line)
		current.WriteString("\n")

		// Track BEGIN...END blocks for triggers.
		upperTrimmed := strings.ToUpper(trimmed)
		if strings.Contains(upperTrimmed, "BEGIN") && !strings.Contains(upperTrimmed, "END") {
			inBeginBlock = true
		}
		if strings.HasSuffix(upperTrimmed, "END;") || strings.HasSuffix(upperTrimmed, "END") {
			inBeginBlock = false
		}

		// Check if line ends with semicolon (statement boundary).
		// Only split if we're not inside a BEGIN block.
		if strings.HasSuffix(trimmed, ";") && !inBeginBlock {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
		}
	}

	// Add any remaining content.
	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

// isBenignSchemaError checks if a schema error can be safely ignored.
// This handles cases like "table already exists" from CREATE IF NOT EXISTS.
func isBenignSchemaError(err error) bool {
	// SQLite returns specific error messages for benign cases.
	errMsg := err.Error()

	// "table already exists" is benign when using CREATE TABLE IF NOT EXISTS.
	if strings.Contains(errMsg, "already exists") {
		return true
	}

	// "trigger already exists" is benign when using CREATE TRIGGER IF NOT EXISTS.
	if strings.Contains(errMsg, "trigger") && strings.Contains(errMsg, "already exists") {
		return true
	}

	return false
}

// truncate truncates a string to maxLen characters for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Close closes the database connection.
// It should be called when the store is no longer needed.
func (s *Store) Close() error {
	s.Logger.Info("closing metadata store")

	if err := s.db.Close(); err != nil {
		s.Logger.Error("failed to close database", "error", err)
		return fmt.Errorf("meta: failed to close database: %w", err)
	}

	s.Logger.Info("metadata store closed")

	return nil
}

// BeginTx starts a new transaction with the given options.
// Use transactions for operations that need atomicity across multiple
// CRUD operations (e.g., creating a session and updating workspace ref count).
//
// Example:
//
//	tx, err := store.BeginTx(ctx)
//	if err != nil { return err }
//	defer tx.Rollback()
//
//	// Do operations with tx...
//	if err := tx.Commit(); err != nil { return err }
func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		s.Logger.Error("failed to begin transaction", "error", err)
		return nil, fmt.Errorf("meta: failed to begin transaction: %w", err)
	}
	return tx, nil
}

// DB returns the underlying database connection for direct queries.
// Use this for operations that don't fit the CRUD pattern.
// Most operations should use the typed CRUD methods instead.
func (s *Store) DB() *sql.DB {
	return s.db
}