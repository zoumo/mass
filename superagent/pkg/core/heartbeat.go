package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
)

const heartbeatFreshness = 30 * time.Second

const heartbeatSchema = `
CREATE TABLE channel_health (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  source TEXT NOT NULL REFERENCES source_bindings(source),
  instance_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  body BLOB NOT NULL,
  UNIQUE(source,instance_id)
);`

// Keep the latest reported facts even after their active status expires.
const channelHealthProjection = `CASE WHEN julianday('now') >= julianday(expires_at)
  THEN json_set(body,'$.status','stale','$.stale',json('true'))
  ELSE json_set(body,'$.stale',json('false')) END`

type heartbeatRequest struct {
	ChannelSnapshot
	OperationID string `json:"operationId"`
}

type ChannelSnapshot struct {
	Source         string `json:"source"`
	AccountID      string `json:"accountId"`
	InstanceID     string `json:"instanceId"`
	Connection     string `json:"connection"`
	Admission      string `json:"admission"`
	LastReceivedAt string `json:"lastReceivedAt,omitempty"`
	LastErrorAt    string `json:"lastErrorAt,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	Consumers      int    `json:"consumers"`
	ReadyConsumers int    `json:"readyConsumers"`
	Backlog        int    `json:"backlog"`
	Capacity       int    `json:"capacity"`
	ConsumerReady  bool   `json:"consumerReady"`
}

type heartbeatReceipt struct {
	Status     string `json:"status"`
	HealthID   string `json:"healthId"`
	AcceptedAt string `json:"acceptedAt"`
	ExpiresAt  string `json:"expiresAt"`
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	source, ok := s.authenticate(r)
	if !ok {
		s.fail(w, problem("unauthorized", "valid source credential is required"))
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		s.fail(w, problem("invalid_argument", "Content-Type must be application/json"))
		return
	}
	var request heartbeatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil || dec.Decode(new(any)) != io.EOF {
		s.fail(w, problem("invalid_argument", "invalid heartbeat JSON"))
		return
	}
	receipt, err := s.store.recordHeartbeat(r.Context(), source, request)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, receipt)
}

func (r *heartbeatRequest) validate(source Source) error {
	if !instanceIDFormat.MatchString(r.InstanceID) || !instanceIDFormat.MatchString(r.OperationID) {
		return problem("invalid_argument", "instanceId and operationId must be valid identifiers")
	}
	if source.InstanceID == "" || r.Source != source.Source || r.AccountID != source.AccountID || r.InstanceID != source.InstanceID {
		return problem("forbidden", "heartbeat identity must match the configured source, account and instance")
	}
	switch r.Connection {
	case "connecting", "ready", "disconnected":
	default:
		return problem("invalid_argument", "invalid connection state")
	}
	switch r.Admission {
	case "waiting", "pending", "accepted", "failed":
	default:
		return problem("invalid_argument", "invalid admission state")
	}
	if r.Consumers < 1 || r.ReadyConsumers < 0 || r.ReadyConsumers > r.Consumers ||
		r.ConsumerReady != (r.Connection == "ready") || r.ConsumerReady != (r.ReadyConsumers == r.Consumers) ||
		(r.Connection == "disconnected" && r.ReadyConsumers != 0) {
		return problem("invalid_argument", "connection and consumer readiness are inconsistent")
	}
	if r.Capacity < 1 || r.Backlog < 0 || r.Backlog > r.Capacity {
		return problem("invalid_argument", "backlog must be between zero and capacity")
	}
	if !heartbeatError(r.LastError) || !heartbeatStop(r.StopReason) ||
		(r.LastError == "") != (r.LastErrorAt == "") || (r.Admission == "failed" && r.LastError == "") ||
		(r.StopReason != "" && r.Connection != "disconnected") {
		return problem("invalid_argument", "invalid heartbeat error or stop state")
	}
	for _, stamp := range []*string{&r.LastReceivedAt, &r.LastErrorAt} {
		if *stamp == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, *stamp)
		if err != nil {
			return problem("invalid_argument", "heartbeat timestamps must be absolute RFC3339 times")
		}
		*stamp = at.UTC().Format(time.RFC3339Nano)
	}
	return nil
}

func heartbeatError(code string) bool {
	switch code {
	case "", "input_rejected", "event_before_ready", "invalid_message", "invalid_request",
		"core_unreachable_or_outcome_unknown", "core_response_unknown", "core_receipt_unknown",
		"invalid_argument", "unauthorized", "forbidden", "not_found", "conflict", "stale_binding",
		"failed_precondition", "unsupported", "unavailable", "outcome_unknown":
		return true
	}
	return heartbeatStop(code)
}

func heartbeatStop(code string) bool {
	switch code {
	case "", "buffer_full", "shutdown", "consumer_ready_timeout", "consumer_pipe_failed",
		"consumer_start_failed", "consumer_exited", "consumer_failed", "dws_connection_error",
		"dws_stderr_invalid", "dws_ndjson_too_large_or_unreadable", "core_rejected":
		return true
	default:
		return false
	}
}

func (r ChannelSnapshot) status() string {
	switch {
	case r.Backlog == r.Capacity || r.StopReason == "buffer_full":
		return "buffer_full"
	case r.Admission == "failed":
		return "admission_failed"
	default:
		return r.Connection
	}
}

func (s *Store) recordHeartbeat(ctx context.Context, source Source, req heartbeatRequest) (heartbeatReceipt, error) {
	var receipt heartbeatReceipt
	if err := req.validate(source); err != nil {
		return receipt, err
	}
	canonical, err := json.Marshal(req.ChannelSnapshot)
	if err != nil {
		return receipt, storageError()
	}
	fingerprint := sha256.Sum256(canonical)
	operation := sha256.Sum256([]byte(req.OperationID))
	operationKey := hex.EncodeToString(operation[:])
	kind := "heartbeat:" + source.InstanceID

	// The one-connection pool serializes this transaction with writes from other Core operations.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var saved, prior []byte
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,receipt FROM operations WHERE source=? AND kind=? AND operation_id=?", source.Source, kind, operationKey).Scan(&prior, &saved)
	if err == nil {
		if !bytes.Equal(prior, fingerprint[:]) {
			return receipt, problem("conflict", "operationId was used with different heartbeat parameters")
		}
		if json.Unmarshal(saved, &receipt) != nil {
			return receipt, storageError()
		}
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return receipt, storageError()
	}

	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM channel_health WHERE source=? AND instance_id=?", source.Source, source.InstanceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = newID("ch")
	} else if err != nil {
		return receipt, storageError()
	}
	at := time.Now().UTC()
	receipt = heartbeatReceipt{
		Status: "accepted", HealthID: id,
		AcceptedAt: at.Format(time.RFC3339Nano),
		ExpiresAt:  at.Add(heartbeatFreshness).Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(struct {
		ChannelSnapshot
		ID         string `json:"id"`
		Status     string `json:"status"`
		AcceptedAt string `json:"acceptedAt"`
		ExpiresAt  string `json:"expiresAt"`
	}{req.ChannelSnapshot, id, req.status(), receipt.AcceptedAt, receipt.ExpiresAt})
	if err != nil {
		return heartbeatReceipt{}, storageError()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_health(id,source,instance_id,created_at,expires_at,body)
VALUES(?,?,?,?,?,?) ON CONFLICT(source,instance_id) DO UPDATE SET expires_at=excluded.expires_at,body=excluded.body`,
		id, source.Source, source.InstanceID, now(), receipt.ExpiresAt, body); err != nil {
		return heartbeatReceipt{}, storageError()
	}
	saved, err = json.Marshal(receipt)
	if err != nil {
		return heartbeatReceipt{}, storageError()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(source,kind,operation_id,fingerprint,receipt) VALUES(?,?,?,?,?)",
		source.Source, kind, operationKey, fingerprint[:], saved); err != nil {
		return heartbeatReceipt{}, storageError()
	}
	if err = tx.Commit(); err != nil {
		return heartbeatReceipt{}, storageError()
	}
	return receipt, nil
}
