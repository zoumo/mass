package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type OutboxCommand struct {
	OperationID    string `json:"operationId"`
	OutboxID       string `json:"outboxId"`
	AttemptID      string `json:"attemptId,omitempty"`
	ClaimID        string `json:"claimId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	ErrorCode      string `json:"errorCode,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
}

type OutboxClaim struct {
	OutboxID       string `json:"outboxId"`
	AttemptID      string `json:"attemptId"`
	ClaimID        string `json:"claimId"`
	Generation     int64  `json:"generation"`
	Mode           string `json:"mode"`
	LeaseExpiresAt string `json:"leaseExpiresAt"`
	TaskID         string `json:"taskId,omitempty"`
}

type outboxClaim struct {
	OutboxClaim
	ReleasedAt string
}

func (c outboxClaim) live(at string) bool {
	return c.ClaimID != "" && c.ReleasedAt == "" && c.LeaseExpiresAt > at
}

type outboxChange struct {
	tx      *sql.Tx
	item    OutboxItem
	at      time.Time
	claim   outboxClaim
	attempt OutboxAttempt
}

func (c *outboxChange) loadAttempt(ctx context.Context, id string) error {
	c.attempt = OutboxAttempt{}
	return readJSON(ctx, c.tx, "SELECT body FROM outbox_attempts WHERE outbox_id=? AND id=?", &c.attempt, c.item.ID, id)
}

func (c *outboxChange) saveAttempt(ctx context.Context) error {
	return putJSON(ctx, c.tx, "UPDATE outbox_attempts SET state=?1,body=?3 WHERE id=?2", c.attempt, c.attempt.State, c.attempt.ID)
}

func readOutboxClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) (outboxClaim, error) {
	var c outboxClaim
	err := tx.QueryRowContext(ctx, `SELECT outbox_id,attempt_id,id,generation,mode,expires_at,COALESCE(released_at,'')
 FROM outbox_claims WHERE `+query, args...).Scan(&c.OutboxID, &c.AttemptID, &c.ClaimID, &c.Generation, &c.Mode, &c.LeaseExpiresAt, &c.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, storageError()
	}
	return c, nil
}

func (c *outboxChange) active(ctx context.Context) (outboxClaim, error) {
	return readOutboxClaim(ctx, c.tx, "outbox_id=? AND released_at IS NULL", c.item.ID)
}

func (c *outboxChange) release(ctx context.Context, claim outboxClaim, at string) error {
	if _, err := c.tx.ExecContext(ctx, "UPDATE outbox_claims SET released_at=? WHERE id=? AND released_at IS NULL", at, claim.ClaimID); err != nil {
		return storageError()
	}
	return nil
}

func (c *outboxChange) expire(ctx context.Context) error {
	active, err := c.active(ctx)
	if err != nil || active.ClaimID == "" || active.live(c.at.Format(utcTimestamp)) {
		return err
	}
	if err := c.loadAttempt(ctx, active.AttemptID); err != nil {
		return err
	}
	switch c.attempt.State {
	case "reserved":
		c.attempt.State = "skipped"
	case "started":
		expiry, parseErr := time.Parse(utcTimestamp, active.LeaseExpiresAt)
		if parseErr != nil {
			return storageError()
		}
		c.attempt.State, c.attempt.ErrorCode = "unknown", "lease_expired_without_result"
		c.attempt.RetryAt = expiry.Add(outboxUnknown).Format(utcTimestamp)
		c.item.PossibleDuplicate = true
	}
	if err := c.saveAttempt(ctx); err != nil {
		return err
	}
	if err := c.release(ctx, active, active.LeaseExpiresAt); err != nil {
		return err
	}
	return c.project(ctx)
}

func (c *outboxChange) begin(ctx context.Context) (OutboxClaim, error) {
	var grant OutboxClaim
	active, err := c.active(ctx)
	if err != nil {
		return grant, err
	}
	if active.ClaimID != "" {
		return grant, problem("conflict", "Outbox already has a current claim")
	}
	var taskAttempt string
	err = c.tx.QueryRowContext(ctx, "SELECT a.id FROM outbox_attempts a WHERE a.outbox_id=? AND "+outboxRecoverableTask+" ORDER BY a.seq LIMIT 1", c.item.ID).Scan(&taskAttempt)
	mode := "recover"
	switch {
	case err == nil:
		if err := c.loadAttempt(ctx, taskAttempt); err != nil {
			return grant, err
		}
		if c.attempt.State == "unknown" {
			c.attempt.State, c.attempt.RetryAt = "task", ""
			if err := c.saveAttempt(ctx); err != nil {
				return grant, err
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return grant, storageError()
	default:
		if c.item.Status != "pending" && (c.item.Status != "unknown" || c.item.RetryAt > c.at.Format(utcTimestamp)) {
			return grant, problem("failed_precondition", "Outbox has no work eligible for a claim")
		}
		mode = "send"
		c.attempt = OutboxAttempt{ID: newID("send"), OutboxID: c.item.ID, State: "reserved", CreatedAt: c.at.Format(utcTimestamp)}
		if err := putJSON(ctx, c.tx, "INSERT INTO outbox_attempts(id,outbox_id,state,body) VALUES(?,?,?,?)", c.attempt, c.attempt.ID, c.item.ID, c.attempt.State); err != nil {
			return grant, err
		}
	}
	grant = OutboxClaim{
		OutboxID: c.item.ID, AttemptID: c.attempt.ID, ClaimID: newID("claim"),
		Mode: mode, LeaseExpiresAt: c.at.Add(outboxLease).Format(utcTimestamp), TaskID: c.attempt.TaskID,
	}
	err = c.tx.QueryRowContext(ctx, `INSERT INTO outbox_claims(id,outbox_id,attempt_id,generation,mode,claimed_at,expires_at)
 VALUES(?,?,?,(SELECT COALESCE(MAX(generation),0)+1 FROM outbox_claims WHERE outbox_id=?),?,?,?) RETURNING generation`,
		grant.ClaimID, c.item.ID, grant.AttemptID, c.item.ID, mode, c.at.Format(utcTimestamp), grant.LeaseExpiresAt).Scan(&grant.Generation)
	if err != nil {
		return grant, storageError()
	}
	return grant, c.project(ctx)
}

func (c *outboxChange) checkClaim(ctx context.Context, req OutboxCommand) error {
	claim, err := readOutboxClaim(ctx, c.tx, "outbox_id=? AND attempt_id=? AND id=?", c.item.ID, req.AttemptID, req.ClaimID)
	if err != nil {
		return err
	}
	if claim.ClaimID == "" {
		return problem("not_found", "claim does not identify an Outbox attempt in current scope")
	}
	c.claim = claim
	if err := c.loadAttempt(ctx, req.AttemptID); err != nil {
		return err
	}
	c.claim.TaskID = c.attempt.TaskID
	return nil
}

func (c *outboxChange) confirm(ctx context.Context) (OutboxClaim, error) {
	if !c.claim.live(c.at.Format(utcTimestamp)) || c.claim.Mode != "send" || c.attempt.State != "reserved" {
		return OutboxClaim{}, problem("conflict", "send requires a live, unused send claim")
	}
	taskExists, err := c.hasRecoverableTask(ctx)
	if err != nil {
		return OutboxClaim{}, err
	}
	if taskExists {
		return OutboxClaim{}, problem("conflict", "existing task must be recovered before another send")
	}
	c.attempt.State, c.attempt.StartedAt = "started", c.at.Format(utcTimestamp)
	return c.claim.OutboxClaim, c.saveAttempt(ctx)
}

func (c *outboxChange) hasRecoverableTask(ctx context.Context) (bool, error) {
	var exists bool
	if err := c.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM outbox_attempts a WHERE a.outbox_id=? AND "+outboxRecoverableTask+")", c.item.ID).Scan(&exists); err != nil {
		return false, storageError()
	}
	return exists, nil
}

func (c *outboxChange) renew(ctx context.Context) (OutboxClaim, error) {
	if !c.claim.live(c.at.Format(utcTimestamp)) {
		return OutboxClaim{}, problem("conflict", "claim was released or expired")
	}
	c.claim.LeaseExpiresAt = c.at.Add(outboxLease).Format(utcTimestamp)
	if _, err := c.tx.ExecContext(ctx, "UPDATE outbox_claims SET expires_at=? WHERE id=?", c.claim.LeaseExpiresAt, c.claim.ClaimID); err != nil {
		return OutboxClaim{}, storageError()
	}
	return c.claim.OutboxClaim, nil
}

func (c *outboxChange) recordTask(ctx context.Context, taskID string) (OutboxAttempt, error) {
	if c.attempt.StartedAt == "" || c.claim.Mode != "send" {
		return c.attempt, problem("failed_precondition", "task requires the claim that started this send")
	}
	if c.attempt.TaskID != "" {
		if c.attempt.TaskID != taskID {
			return c.attempt, problem("conflict", "attempt already has a different taskId")
		}
		return c.attempt, nil
	}
	c.attempt.TaskID, c.attempt.TaskAt = taskID, c.at.Format(utcTimestamp)
	if c.attempt.State == "started" || c.attempt.State == "unknown" {
		c.attempt.State, c.attempt.ErrorCode, c.attempt.RetryAt = "task", "", ""
	}
	if err := c.saveAttempt(ctx); err != nil {
		return c.attempt, err
	}
	if c.attempt.State == "task" {
		if err := c.skipReserved(ctx, c.attempt.ID); err != nil {
			return c.attempt, err
		}
	}
	return c.attempt, c.project(ctx)
}

func (c *outboxChange) finish(ctx context.Context, req OutboxCommand) (OutboxAttempt, error) {
	if c.attempt.StartedAt == "" {
		return c.attempt, problem("failed_precondition", "attempt has no confirmed send")
	}
	result := OutboxResult{
		Outcome: req.Outcome, ErrorCode: req.ErrorCode, ConversationID: req.ConversationID,
		MessageID: req.MessageID, Generation: c.claim.Generation, At: c.at.Format(utcTimestamp),
	}
	c.attempt.Results = append(c.attempt.Results, result)
	var newest int64
	if err := c.tx.QueryRowContext(ctx, "SELECT MAX(generation) FROM outbox_claims WHERE attempt_id=?", c.attempt.ID).Scan(&newest); err != nil {
		return c.attempt, storageError()
	}
	switch {
	case req.Outcome == "sent":
		c.attempt.State, c.attempt.ErrorCode, c.attempt.RetryAt = "sent", "", ""
	case newest == c.claim.Generation && (c.attempt.State == "started" || c.attempt.State == "task" ||
		(c.attempt.State == "unknown" && (req.Outcome != "unknown" || c.attempt.TaskID != ""))):
		c.attempt.ErrorCode, c.attempt.RetryAt = req.ErrorCode, ""
		switch req.Outcome {
		case "unknown":
			if c.attempt.TaskID != "" {
				c.attempt.State = "task"
			} else {
				c.attempt.State = "unknown"
				c.attempt.RetryAt = c.at.Add(outboxUnknown).Format(utcTimestamp)
				c.item.PossibleDuplicate = true
			}
		case "retryable_failure":
			c.attempt.State, c.attempt.RetryAt = "failed", result.At
		case "permanent_failure":
			c.attempt.State = "failed"
		}
	}
	if err := c.saveAttempt(ctx); err != nil {
		return c.attempt, err
	}
	active, err := c.active(ctx)
	if err != nil {
		return c.attempt, err
	}
	if active.AttemptID == c.attempt.ID && (active.ClaimID == c.claim.ClaimID || req.Outcome == "sent") {
		if err := c.release(ctx, active, result.At); err != nil {
			return c.attempt, err
		}
	}
	if req.Outcome == "sent" {
		if err := c.skipReserved(ctx, c.attempt.ID); err != nil {
			return c.attempt, err
		}
	}
	if err := c.project(ctx); err != nil {
		return c.attempt, err
	}
	return c.attempt, reconcileEchoResult(ctx, c.tx, c.item, c.attempt.ID, result)
}

// A known task or success can preempt a send reservation. After confirmation
// the channel may already have observed a side effect, so keep that attempt.
func (c *outboxChange) skipReserved(ctx context.Context, except string) error {
	active, err := c.active(ctx)
	if err != nil || active.ClaimID == "" || active.Mode != "send" || active.AttemptID == except {
		return err
	}
	result, err := c.tx.ExecContext(ctx, `UPDATE outbox_attempts SET state='skipped',body=json_set(body,'$.state','skipped')
 WHERE id=? AND state='reserved'`, active.AttemptID)
	if err != nil {
		return storageError()
	}
	n, err := result.RowsAffected()
	if err != nil {
		return storageError()
	}
	if n > 0 {
		return c.release(ctx, active, c.at.Format(utcTimestamp))
	}
	return nil
}

func (c *outboxChange) project(ctx context.Context) error {
	var latest OutboxAttempt
	err := readJSON(ctx, c.tx, "SELECT body FROM outbox_attempts WHERE outbox_id=? AND state<>'skipped' ORDER BY seq DESC LIMIT 1", &latest, c.item.ID)
	var p *Error
	if err != nil && (!errors.As(err, &p) || p.Code != "not_found") {
		return err
	}
	c.item.CurrentAttempt = latest.ID
	var sent, errorsCount, viable int
	if err = c.tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE state='sent'),
 count(*) FILTER(WHERE state IN ('failed','unknown')), count(*) FILTER(WHERE state IN ('sent','task','started','unknown'))
 FROM outbox_attempts WHERE outbox_id=?`, c.item.ID).Scan(&sent, &errorsCount, &viable); err != nil {
		return storageError()
	}
	c.item.RetryAt, c.item.ErrorCode = "", ""
	switch {
	case sent > 0:
		c.item.Status = "sent"
		c.item.PossibleDuplicate = c.item.PossibleDuplicate || viable > 1
	case c.item.FailureEventID != "":
		c.item.Status = "failed"
		if err = c.tx.QueryRowContext(ctx, "SELECT json_extract(body,'$.payload.errorCode') FROM events WHERE id=?", c.item.FailureEventID).Scan(&c.item.ErrorCode); err != nil {
			return storageError()
		}
	case latest.State == "unknown" || latest.State == "failed":
		c.item.Status, c.item.ErrorCode, c.item.RetryAt = "unknown", latest.ErrorCode, latest.RetryAt
		taskExists, err := c.hasRecoverableTask(ctx)
		if err != nil {
			return err
		}
		switch {
		case (latest.State == "failed" && latest.RetryAt == "") || errorsCount >= outboxMaxErrors:
			c.item.Status, c.item.RetryAt = "failed", ""
			if err := c.notifyFailure(ctx, latest.ID); err != nil {
				return err
			}
		case taskExists:
			c.item.Status, c.item.RetryAt = "sending", ""
		case latest.State == "failed":
			c.item.Status = "pending"
		}
	case latest.ID != "":
		c.item.Status, c.item.ErrorCode = "sending", latest.ErrorCode
	default:
		c.item.Status = "pending"
	}
	return putJSON(ctx, c.tx, "UPDATE outbox SET body=?2 WHERE id=?1", c.item, c.item.ID)
}

func (c *outboxChange) notifyFailure(ctx context.Context, attemptID string) error {
	event := Event{
		ID: newID("evt"), Type: "outbox.failed", At: c.at.Format(utcTimestamp), TargetRole: c.item.AuthorRole,
		TargetID: c.item.AuthorID, Intent: "act",
	}
	var err error
	event.Payload, err = json.Marshal(map[string]string{
		"outboxId": c.item.ID, "messageId": c.item.MessageID,
		"attemptId": attemptID, "errorCode": c.item.ErrorCode,
	})
	if err != nil {
		return storageError()
	}
	if err := EnqueueEvent(ctx, c.tx, event); err != nil {
		return err
	}
	c.item.FailureEventID = event.ID
	return nil
}
