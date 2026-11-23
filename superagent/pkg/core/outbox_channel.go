package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func decodeOutboxResult(w http.ResponseWriter, r *http.Request, request any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return problem("invalid_argument", "Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err = dec.Decode(request); err != nil || dec.Decode(new(any)) != io.EOF {
		return problem("invalid_argument", "invalid Outbox command JSON")
	}
	return nil
}

func (s *Server) authenticateOutboxConsumer(r *http.Request) (Source, error) {
	source, ok := s.authenticate(r)
	if !ok {
		return Source{}, problem("unauthorized", "valid source credential is required")
	}
	if source.InstanceID == "" || s.store.config.outboxConsumer(source.Source, source.AccountID) != source.InstanceID {
		return Source{}, problem("forbidden", "Outbox requires the configured account consumer")
	}
	return source, nil
}

func (s *Server) readOutboxDelivery(w http.ResponseWriter, r *http.Request, id string, list bool, source Source) {
	var result any
	var err error
	if list {
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
		result, err = s.store.getScoped(r.Context(), "outbox", id, nil, &source)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, result)
}

type outboxWriteReceipt struct {
	ID string `json:"id"`
	OutboxState
}

func (s *Server) writeOutboxResult(w http.ResponseWriter, r *http.Request, source Source, req OutboxCommand) {
	if err := req.validate(); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.store.changeOutbox(r.Context(), source, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, outboxWriteReceipt{ID: result.ID, OutboxState: result.OutboxState})
}

type OutboxCommand struct {
	OutboxID  string      `json:"outboxId"`
	Expected  OutboxState `json:"expected"`
	Outcome   string      `json:"outcome"`
	TaskID    string      `json:"taskId,omitempty"`
	ErrorCode string      `json:"errorCode,omitempty"`
}

const outboxRetryDelay = time.Minute

func (r OutboxCommand) validate() error {
	if r.OutboxID == "" || (r.Expected.Status != "pending" && r.Expected.Status != "failed") {
		return problem("invalid_argument", "outboxId and expected pending or failed state are required")
	}
	if r.TaskID != "" && strings.TrimSpace(r.TaskID) == "" {
		return problem("invalid_argument", "taskId cannot be blank")
	}
	switch r.Outcome {
	case "pending":
		if r.TaskID == "" || r.ErrorCode != "" {
			return problem("invalid_argument", "pending requires a taskId alone")
		}
	case "sent":
		if r.ErrorCode != "" {
			return problem("invalid_argument", "sent omits errorCode")
		}
	case "retryable_failure", "permanent_failure", "unknown":
		if !instanceIDFormat.MatchString(r.ErrorCode) {
			return problem("invalid_argument", "unsuccessful outcomes require a safe errorCode")
		}
	default:
		return problem("invalid_argument", "invalid Outbox outcome")
	}
	return nil
}

func (r OutboxCommand) result(at time.Time) OutboxState {
	state := OutboxState{Status: r.Outcome}
	receipt := OutboxReceipt{
		TaskID: r.TaskID, ErrorCode: r.ErrorCode,
		Uncertain: receiptValue(r.Expected).Uncertain || r.Outcome == "unknown",
	}
	switch r.Outcome {
	case "unknown", "retryable_failure", "permanent_failure":
		state.Status = "failed"
		if r.Outcome != "permanent_failure" {
			receipt.RetryAt = at.Add(outboxRetryDelay).Format(utcTimestamp)
		}
	}
	if receipt != (OutboxReceipt{}) {
		state.Receipt = &receipt
	}
	return state
}

func receiptValue(state OutboxState) OutboxReceipt {
	if state.Receipt == nil {
		return OutboxReceipt{}
	}
	return *state.Receipt
}

func (s *Store) changeOutbox(ctx context.Context, source Source, req OutboxCommand) (item OutboxItem, retErr error) {
	ctx, finish := s.telemetry.startOperation(ctx, "outbox.settle", "")
	outcome := req.Outcome // The channel handler validates the finite outcome set.
	defer func() { finish(operationOutcome(retErr, outcome)) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := readJSON(ctx, tx, "SELECT "+outboxProjection+" FROM outbox WHERE id=?"+channelOutboxScope,
		&item, req.OutboxID, source.Source, source.AccountID); err != nil {
		return item, err
	}
	if s.telemetry.tracesEnabled {
		addTraceLinks(ctx, outboxEnqueue.links(ctx, tx, nil, item.ID, 0))
	}
	at := outboxNow()
	current, expected := receiptValue(item.OutboxState), receiptValue(req.Expected)
	next := req.result(at)
	expectedResult := receiptValue(next)
	if item.Status != req.Expected.Status || current != expected {
		// The server sets retryAt. An otherwise identical replay returns the
		// stored deadline and has no effect on later polling.
		currentForReplay := current
		if currentForReplay.RetryAt != "" && expectedResult.RetryAt != "" {
			currentForReplay.RetryAt = expectedResult.RetryAt
		}
		if item.Status == next.Status && currentForReplay == expectedResult {
			outcome = "duplicate"
			return item, nil
		}
		return item, problem("conflict", "Outbox receipt changed since the pull")
	}
	if item.Status == "failed" && (current.RetryAt == "" || current.RetryAt > at.Format(utcTimestamp)) {
		return item, problem("failed_precondition", "Outbox is not eligible for retry")
	}
	if item.Status == "pending" && current.TaskID != "" && current.TaskID != req.TaskID {
		return item, problem("conflict", "result must refer to the recorded channel task")
	}
	if item.Status == next.Status && current == expectedResult {
		outcome = "duplicate"
		return item, nil
	}
	var rawReceipt any
	if next.Receipt != nil {
		raw, err := json.Marshal(next.Receipt)
		if err != nil {
			return item, storageError()
		}
		rawReceipt = string(raw)
	}
	item.OutboxState = next
	if _, err := tx.ExecContext(ctx, "UPDATE outbox SET status=?,receipt=? WHERE id=?", item.Status, rawReceipt, item.ID); err != nil {
		return item, storageError()
	}
	if req.Outcome == "permanent_failure" {
		if err := enqueueOutboxFailure(ctx, tx, item); err != nil {
			return item, err
		}
	}
	commitGate("outbox_result_before_commit")
	if err := tx.Commit(); err != nil {
		return item, storageError()
	}
	commitGate("outbox_result_after_commit")
	return item, nil
}

func enqueueOutboxFailure(ctx context.Context, tx *sql.Tx, item OutboxItem) error {
	var recipient eventIdentity
	err := tx.QueryRowContext(ctx, `SELECT b.role,b.owner_id FROM outbox JOIN session_bindings b
 ON b.id=`+outboxAuthorID+` WHERE outbox.id=?`, item.ID).Scan(&recipient.Kind, &recipient.TopicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageError()
	}
	body, err := json.Marshal(map[string]string{"errorCode": item.Receipt.ErrorCode})
	if err != nil {
		return storageError()
	}
	return enqueueInternalEvent(ctx, tx, internalEvent{
		ID: newID("evt"), Kind: "outbox_failed", Sender: eventIdentity{Kind: "core"}, Recipient: recipient,
		Body: body, Refs: []eventReference{
			{Kind: "outbox", ID: item.ID}, {Kind: messageRefKind, ID: item.MessageID},
		},
	})
}
