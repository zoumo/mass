package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) channelOutbox(w http.ResponseWriter, r *http.Request) {
	source, ok := s.authenticate(r)
	if !ok {
		s.fail(w, problem("unauthorized", "valid source credential is required"))
		return
	}
	if source.InstanceID == "" {
		s.fail(w, problem("forbidden", "Outbox requires an instance-bound credential"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/channel/outbox")
	switch {
	case path == "" || strings.HasPrefix(path, "/items/"):
		if !readOnly(w, r) {
			return
		}
		var result any
		var err error
		if path == "" {
			q := r.URL.Query()
			limit := 50
			if value := q.Get("limit"); value != "" {
				limit, err = strconv.Atoi(value)
				if err != nil {
					s.fail(w, problem("invalid_argument", "limit must be an integer"))
					return
				}
			}
			result, err = s.store.listScoped(r.Context(), QueryRequest{
				Collection: "outbox", Cursor: q.Get("cursor"), Limit: limit,
			}, nil, &source)
		} else {
			result, err = s.store.getScoped(r.Context(), "outbox", strings.TrimPrefix(path, "/items/"), nil, &source)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, result)
	default:
		s.outboxCommand(w, r, source, strings.TrimPrefix(path, "/"))
	}
}

func (s *Server) outboxCommand(w http.ResponseWriter, r *http.Request, source Source, command string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		s.fail(w, problem("invalid_argument", "Content-Type must be application/json"))
		return
	}
	var req OutboxCommand
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		s.fail(w, problem("invalid_argument", "invalid Outbox command JSON"))
		return
	}
	if err = req.validate(command); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.store.changeOutbox(r.Context(), source, command, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, result)
}

func (r OutboxCommand) validate(command string) error {
	if !instanceIDFormat.MatchString(r.OperationID) || strings.TrimSpace(r.OutboxID) == "" {
		return problem("invalid_argument", "operationId and outboxId are required")
	}
	if command == "begin-attempt" {
		if r.AttemptID != "" || r.ClaimID != "" {
			return problem("invalid_argument", "beginAttempt takes only operationId and outboxId")
		}
	} else if strings.TrimSpace(r.AttemptID) == "" || strings.TrimSpace(r.ClaimID) == "" {
		return problem("invalid_argument", "attemptId and claimId are required")
	}
	switch command {
	case "begin-attempt", "confirm-send", "renew-lease", "record-task", "finish-attempt":
	default:
		return problem("not_found", "unknown Outbox command")
	}
	if command == "record-task" {
		if strings.TrimSpace(r.TaskID) == "" {
			return problem("invalid_argument", "taskId is required")
		}
	} else if r.TaskID != "" {
		return problem("invalid_argument", "taskId belongs in recordTask")
	}
	if command != "finish-attempt" {
		if r.Outcome != "" || r.ErrorCode != "" || r.MessageID != "" || r.ConversationID != "" {
			return problem("invalid_argument", "result fields belong in finishAttempt")
		}
		return nil
	}
	switch r.Outcome {
	case "sent":
		if r.ErrorCode != "" || (r.MessageID != "" && (strings.TrimSpace(r.MessageID) == "" || strings.TrimSpace(r.ConversationID) == "")) ||
			(r.ConversationID != "" && strings.TrimSpace(r.ConversationID) == "") {
			return problem("invalid_argument", "sent must omit errorCode and provide conversationId with any actual messageId")
		}
	case "retryable_failure", "permanent_failure", "unknown":
		if !instanceIDFormat.MatchString(r.ErrorCode) || r.MessageID != "" || r.ConversationID != "" {
			return problem("invalid_argument", "unsuccessful outcomes require an errorCode and omit actual message references")
		}
	default:
		return problem("invalid_argument", "outcome must be sent, retryable_failure, permanent_failure, or unknown")
	}
	return nil
}

func (s *Store) changeOutbox(ctx context.Context, source Source, command string, req OutboxCommand) (json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	change := outboxChange{tx: tx, at: outboxNow()}
	if err := readJSON(ctx, tx, "SELECT body FROM outbox WHERE id=? AND source=? AND instance_id=?", &change.item, req.OutboxID, source.Source, source.InstanceID); err != nil {
		return nil, err
	}
	rawReq, err := json.Marshal(req)
	if err != nil {
		return nil, storageError()
	}
	fingerprint := sha256.Sum256(rawReq)
	kind := "outbox:" + command + ":" + source.InstanceID
	var saved, fp []byte
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,receipt FROM operations WHERE source=? AND kind=? AND operation_id=?",
		source.Source, kind, req.OperationID).Scan(&fp, &saved)
	if err == nil {
		if !bytes.Equal(fp, fingerprint[:]) {
			return nil, problem("conflict", "operationId has different parameters")
		}
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError()
	}
	// Success and task receipts can settle at any time, including immediately
	// after lease expiry. Apply them before considering timeout escalation.
	if command == "begin-attempt" {
		if err := change.expire(ctx); err != nil {
			return nil, err
		}
	} else if err := change.checkClaim(ctx, req); err != nil {
		return nil, err
	}
	var result any
	switch command {
	case "begin-attempt":
		result, err = change.begin(ctx)
	case "confirm-send":
		result, err = change.confirm(ctx)
	case "renew-lease":
		result, err = change.renew(ctx)
	case "record-task":
		result, err = change.recordTask(ctx, req.TaskID)
	case "finish-attempt":
		result, err = change.finish(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, storageError()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(source,kind,operation_id,fingerprint,receipt) VALUES(?,?,?,?,?)",
		source.Source, kind, req.OperationID, fingerprint[:], raw); err != nil {
		return nil, storageError()
	}
	commitGate("outbox_" + command + "_before_commit")
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	commitGate("outbox_" + command + "_after_commit")
	return raw, nil
}

func (s *Store) outboxMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.expireOutboxClaims(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Outbox lease reconciliation failed", "error", err)
		}
		if err := s.expireEchoHolds(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Echo hold reconciliation failed", "error", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Store) expireOutboxClaims(ctx context.Context) error {
	at := outboxNow()
	rows, err := s.db.QueryContext(ctx, `SELECT outbox_id FROM outbox_claims
 WHERE released_at IS NULL AND expires_at<=? ORDER BY expires_at,outbox_id LIMIT 100`, at.Format(utcTimestamp))
	if err != nil {
		return storageError()
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return storageError()
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil || rows.Err() != nil {
		return storageError()
	}
	for _, id := range ids {
		if err := s.expireOutboxClaim(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) expireOutboxClaim(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	change := outboxChange{tx: tx, at: outboxNow()}
	if err := readJSON(ctx, tx, "SELECT body FROM outbox WHERE id=?", &change.item, id); err != nil {
		return err
	}
	if err := change.expire(ctx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}
