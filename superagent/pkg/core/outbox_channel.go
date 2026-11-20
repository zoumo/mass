package core

import (
	"context"
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
	if source.InstanceID == "" || s.store.config.outboxConsumer(source.Source, source.AccountID) != source.InstanceID {
		s.fail(w, problem("forbidden", "Outbox requires the configured account consumer"))
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
	case path == "/result":
		s.outboxResult(w, r, source)
	default:
		s.fail(w, problem("not_found", "unknown Outbox path"))
	}
}

func (s *Server) outboxResult(w http.ResponseWriter, r *http.Request, source Source) {
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		s.fail(w, problem("invalid_argument", "invalid Outbox command JSON"))
		return
	}
	if err = req.validate(); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.store.changeOutbox(r.Context(), source, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, struct {
		ID string `json:"id"`
		OutboxState
	}{ID: result.ID, OutboxState: result.OutboxState})
}

type OutboxCommand struct {
	OutboxID       string      `json:"outboxId"`
	Expected       OutboxState `json:"expected"`
	Outcome        string      `json:"outcome"`
	TaskID         string      `json:"taskId,omitempty"`
	ErrorCode      string      `json:"errorCode,omitempty"`
	ConversationID string      `json:"conversationId,omitempty"`
	MessageID      string      `json:"messageId,omitempty"`
}

const outboxRetryDelay = time.Minute

func (r OutboxCommand) validate() error {
	if r.OutboxID == "" || (r.Expected.Status != "pending" && r.Expected.Status != "failed") {
		return problem("invalid_argument", "outboxId and expected pending or failed state are required")
	}
	for _, id := range []string{r.TaskID, r.ConversationID, r.MessageID} {
		if id != "" && strings.TrimSpace(id) == "" {
			return problem("invalid_argument", "channel identities cannot be blank")
		}
	}
	switch r.Outcome {
	case "pending":
		if r.TaskID == "" || r.ErrorCode != "" || r.ConversationID != "" || r.MessageID != "" {
			return problem("invalid_argument", "pending requires a taskId alone")
		}
	case "sent":
		if r.ErrorCode != "" || (r.MessageID != "" && r.ConversationID == "") {
			return problem("invalid_argument", "sent requires a conversationId with any messageId and omits errorCode")
		}
	case "retryable_failure", "permanent_failure", "unknown":
		if !instanceIDFormat.MatchString(r.ErrorCode) || r.MessageID != "" || r.ConversationID != "" {
			return problem("invalid_argument", "unsuccessful outcomes require a safe errorCode and omit native message references")
		}
	default:
		return problem("invalid_argument", "invalid Outbox outcome")
	}
	return nil
}

func (r OutboxCommand) result(at time.Time) OutboxState {
	state := OutboxState{Status: r.Outcome}
	receipt := OutboxReceipt{
		TaskID: r.TaskID, ErrorCode: r.ErrorCode, ConversationID: r.ConversationID, MessageID: r.MessageID,
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

func (s *Store) changeOutbox(ctx context.Context, source Source, req OutboxCommand) (OutboxItem, error) {
	var item OutboxItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := readJSON(ctx, tx, "SELECT "+outboxProjection+" FROM outbox WHERE id=?"+channelOutboxScope,
		&item, req.OutboxID, source.Source, source.AccountID); err != nil {
		return item, err
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
	if err := reconcileEchoResult(ctx, tx, item, source, at); err != nil {
		return item, err
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
	var recipient deliveryIdentity
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
	return enqueueInternalDelivery(ctx, tx, internalDelivery{
		ID: newID("dlv"), Kind: "outbox_failed", Sender: deliveryIdentity{Kind: "core"}, Recipient: recipient,
		Body: body, Refs: []deliveryReference{
			{Kind: "outbox", ID: item.ID}, {Kind: messageRefKind, ID: item.MessageID},
		},
	})
}

func (s *Store) outboxMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
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
