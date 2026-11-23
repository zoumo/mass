package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
)

type RecoveryRequest struct {
	OperationID             string `json:"operationId"`
	SessionBindingID        string `json:"sessionBindingId"`
	Mode                    string `json:"mode"`
	Reconciliation          string `json:"reconciliation"`
	AcceptPossibleDuplicate bool   `json:"acceptPossibleDuplicate"`
	StopRuntime             bool   `json:"stopRuntime,omitempty"`
}

type bindingRecoveryInput struct {
	OperationID             string `json:"operationId" pattern:"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$"`
	Mode                    string `json:"mode" enum:"retry,replace,settle"`
	Reconciliation          string `json:"reconciliation" minLength:"1" maxLength:"4096"`
	AcceptPossibleDuplicate bool   `json:"acceptPossibleDuplicate" doc:"Must be true"`
	StopRuntime             bool   `json:"stopRuntime,omitempty"`
}

type Recovery struct {
	RecoveryRequest
	ID                          string                  `json:"id"`
	CreatedAt                   string                  `json:"createdAt"`
	CompletedAt                 string                  `json:"completedAt,omitempty"`
	Status                      string                  `json:"status"`
	LastError                   string                  `json:"lastError,omitempty"`
	SessionID                   string                  `json:"sessionId,omitempty"`
	SessionUID                  string                  `json:"sessionUid,omitempty"`
	ClosedResource              string                  `json:"closedResourceVersion,omitempty"`
	ResumedResource             string                  `json:"resumedResourceVersion,omitempty"`
	ReplacementSessionBindingID string                  `json:"replacementSessionBindingId,omitempty"`
	Close                       *BindingClose           `json:"close,omitempty"`
	RelatedReleases             []RuntimeBindingRelease `json:"relatedReleases,omitempty"`
}

func ReadOperatorToken(path string) (string, error) {
	data, err := readPrivate(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 || strings.ContainsAny(token, " \r\n\t") {
		return "", problem("invalid_argument", "operator token requires at least 32 non-whitespace bytes")
	}
	return token, nil
}

func (s *Server) recoverInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeOperator(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var req RecoveryRequest
	if err != nil || decodeStrict(data, &req) != nil {
		s.fail(w, problem("invalid_argument", "invalid recovery request"))
		return
	}
	s.applyInputRecovery(w, r, req)
}

func (s *Server) recoverBindingInput(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorizeOperator(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var input bindingRecoveryInput
	if err != nil || decodeStrict(data, &input) != nil {
		s.fail(w, problem("invalid_argument", "invalid recovery request"))
		return
	}
	s.applyInputRecovery(w, r, RecoveryRequest{
		SessionBindingID: id, OperationID: input.OperationID, Mode: input.Mode,
		Reconciliation: input.Reconciliation, AcceptPossibleDuplicate: input.AcceptPossibleDuplicate, StopRuntime: input.StopRuntime,
	})
}

func (s *Server) applyInputRecovery(w http.ResponseWriter, r *http.Request, req RecoveryRequest) {
	if !instanceIDFormat.MatchString(req.OperationID) ||
		req.SessionBindingID == "" || (req.Mode != "retry" && req.Mode != "replace" && req.Mode != "settle") ||
		req.Mode == "retry" && req.StopRuntime ||
		strings.TrimSpace(req.Reconciliation) == "" || len(req.Reconciliation) > 4096 || !req.AcceptPossibleDuplicate {
		s.fail(w, problem("invalid_argument", "operationId, sessionBindingId, retry/replace/settle, reconciliation and acknowledgement of possible duplication are required"))
		return
	}
	if req.StopRuntime {
		if !s.runtimeOperations.TryLock() {
			s.fail(w, problem("conflict", "Core has Session operations in flight; retry after they settle"))
			return
		}
		defer s.runtimeOperations.Unlock()
	} else {
		if !s.runtimeOperations.TryRLock() {
			s.fail(w, problem("conflict", "Core is settling a Runtime"))
			return
		}
		defer s.runtimeOperations.RUnlock()
	}
	if _, busy := s.bindingOperations.LoadOrStore(req.SessionBindingID, struct{}{}); busy {
		s.fail(w, problem("conflict", "binding has a Core operation in flight"))
		return
	}
	defer s.bindingOperations.Delete(req.SessionBindingID)
	recovery, b, spec, err := s.store.beginRecovery(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	if recovery.Status != "completed" {
		// Once recorded, recovery owns its bounded external call and readback,
		// even if the operator's HTTP connection disconnects.
		operation := context.WithoutCancel(r.Context())
		call, cancel := context.WithTimeout(operation, 7*time.Second)
		if recovery.Mode == "retry" {
			err = s.reconcileSession(call, b, spec, &recovery)
		} else {
			err = s.reconcileRetirement(call, b, spec, &recovery)
		}
		cancel()
		if err == nil {
			saveCtx, cancelSave := context.WithTimeout(operation, time.Second)
			recovery, err = s.store.completeRecovery(saveCtx, b, recovery)
			cancelSave()
		}
		if err != nil {
			recovery.Status, recovery.LastError = "blocked", err.Error()
			saveCtx, cancelSave := context.WithTimeout(operation, time.Second)
			_ = s.store.saveRecoveryFailure(saveCtx, recovery)
			cancelSave()
			s.fail(w, err)
			return
		}
	}
	s.write(w, http.StatusOK, recovery)
}

func (s *Store) beginRecovery(ctx context.Context, req RecoveryRequest) (Recovery, SessionBinding, resource.SessionSpec, error) {
	var r Recovery
	var b SessionBinding
	var spec resource.SessionSpec
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, b, spec, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := readJSON(ctx, tx, "SELECT body FROM session_bindings WHERE id=?", &b, req.SessionBindingID); err != nil {
		return r, b, spec, err
	}
	known := b.Recovery != nil && b.Recovery.ID == req.OperationID
	if known {
		r = *b.Recovery
		if r.RecoveryRequest != req {
			return r, b, spec, problem("conflict", "operationId already has different recovery input")
		}
		if r.Status == "completed" {
			return r, b, spec, nil
		}
	}
	retired := b.State == "revoked"
	if retired {
		if req.Mode == "retry" || b.Role == "router" {
			return r, b, spec, problem("failed_precondition", "retired bindings permit only Issue Session or Topic settlement")
		}
		stopping := b.Stop != nil && (b.Stop.Status == "requested" || b.Stop.Status == "dispatching")
		if stopping {
			return r, b, spec, problem("failed_precondition", "the binding's cancellation request is still queued or in flight")
		}
	} else {
		if req.Mode == "settle" {
			return r, b, spec, problem("failed_precondition", "settle requires a retired binding")
		}
		if known && b.State != "recovering" {
			return r, b, spec, problem("stale_binding", "recovery operation no longer owns this binding")
		}
	}
	if !known && b.Recovery != nil && b.Recovery.Status == "reconciling" {
		return r, b, spec, problem("failed_precondition", "retry the in-flight recovery with its original operationId")
	}
	if err := readJSON(ctx, tx, "SELECT spec FROM session_bindings WHERE id=?", &spec, b.ID); err != nil {
		return r, b, spec, err
	}
	if !known {
		if req.Mode == "retry" {
			var blocked bool
			err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM internal_events WHERE "+eventRecipientKind+"=? AND "+eventRecipientOwner+"=? AND status='dispatching')", b.Role, b.eventRecipientID()).Scan(&blocked)
			if err != nil {
				return r, b, spec, storageError()
			}
			if !blocked && b.State != "blocked" && b.State != "recovering" {
				return r, b, spec, problem("failed_precondition", "binding has no explicitly unknown input to reconcile")
			}
		}
		r = Recovery{ID: req.OperationID, RecoveryRequest: req, CreatedAt: now(), Status: "reconciling", SessionID: b.SessionID, SessionUID: b.SessionUID}
		b.Recovery = &r
		if !retired {
			b.State, b.NextAttemptAt = "recovering", ""
		}
		if err := putJSON(ctx, tx, "UPDATE session_bindings SET state=?2,next_attempt_at=?3,body=?4 WHERE id=?1",
			b, b.ID, b.State, b.NextAttemptAt); err != nil {
			return r, b, spec, err
		}
	}
	return r, b, spec, tx.Commit()
}

func (s *Server) reconcileSession(ctx context.Context, b SessionBinding, spec resource.SessionSpec, r *Recovery) error {
	c, err := s.massClient()
	if err != nil {
		return problem("unavailable", "cannot connect to massd")
	}
	defer func() { _ = c.Close() }()
	ref := bindingSessionRef(b)
	var found resource.Session
	if err := c.GetSession(ctx, ref, &found); err != nil || !bindingMatches(b, spec, found) {
		return problem("failed_precondition", "cannot verify original Session identity and configuration in massd")
	}
	ref = resource.SessionRef{SessionID: found.Status.SessionID}
	switch found.Status.State {
	case "offline", "unknown":
		// The same authority may use the public resume path. Neither state
		// certifies old work stopped or allows a replacement binding.
		if ownershipUnconfirmed(found) {
			affected, err := s.store.db.ExecContext(ctx, `UPDATE session_bindings SET body=json_set(body,'$.previousOwnerUnknown',json('true'))
 WHERE id=? AND state='recovering'`, b.ID)
			if err != nil {
				return storageError()
			}
			if count, err := affected.RowsAffected(); err != nil || count != 1 {
				return problem("stale_binding", "binding changed before recovery")
			}
		}
	default:
		if !sessionOwnsClose(found) {
			return problem("failed_precondition", "the original native Session owner is unconfirmed")
		}
		closed, err := closeOriginalSession(ctx, c, b, spec, ref)
		if err != nil {
			return err
		}
		r.ClosedResource = closed.Metadata.ResourceVersion
	}
	var resumed resource.Session
	if err := c.ResumeSession(ctx, ref, &resumed); err != nil || !bindingMatches(b, spec, resumed) {
		return problem("failed_precondition", "massd could not resume the verified original Session")
	}
	if err := c.GetSession(ctx, ref, &found); err != nil || !bindingMatches(b, spec, found) || !ownedOnlineSession(found) {
		return problem("failed_precondition", "massd did not confirm the resumed Session binding")
	}
	r.ResumedResource = found.Metadata.ResourceVersion
	r.SessionID, r.SessionUID = found.Status.SessionID, found.Metadata.UID
	return nil
}

func sessionOwnsClose(session resource.Session) bool {
	return ownedOnlineSession(session) || session.Status.State == "closing" && session.Status.AppliedGeneration != 0
}

func closeOriginalSession(ctx context.Context, c *client.Client, b SessionBinding, spec resource.SessionSpec, ref resource.SessionRef) (resource.Session, error) {
	var closed resource.Session
	if err := c.CloseSession(ctx, ref, &closed); err != nil || closed.Status.State != "offline" || !bindingMatches(b, spec, closed) {
		return closed, problem("failed_precondition", "massd did not confirm close of the original Session")
	}
	var readback resource.Session
	if err := c.GetSession(ctx, ref, &readback); err != nil || readback.Status.State != "offline" || !bindingMatches(b, spec, readback) {
		return readback, problem("failed_precondition", "original Session is not verified offline after close")
	}
	return readback, nil
}

func ownedOnlineSession(session resource.Session) bool {
	switch session.Status.State {
	case "idle", "running", "requires_action":
		return session.Status.AppliedGeneration != 0
	default:
		return false
	}
}

func (s *Server) reconcileRetirement(ctx context.Context, b SessionBinding, spec resource.SessionSpec, r *Recovery) error {
	if b.PreviousOwnerUnknown {
		return problem("failed_precondition", "a previously unreachable native owner remains unconfirmed after same-binding recovery")
	}
	if b.Close != nil {
		r.Close, r.ClosedResource = b.Close, b.Close.ResourceVersion
		return nil
	}
	if !b.PreparationStarted {
		r.Close = &BindingClose{At: now(), Method: "preparation_never_started"}
		return nil
	}
	c, err := s.massClient()
	if err != nil {
		return problem("unavailable", "cannot connect to massd")
	}
	defer func() { _ = c.Close() }()
	ref := bindingSessionRef(b)
	var found resource.Session
	if err := c.GetSession(ctx, ref, &found); err != nil || !bindingMatches(b, spec, found) {
		return problem("failed_precondition", "cannot verify original Session identity and configuration in massd")
	}
	if found.Status.State == "unknown" || found.Status.State == "opening" {
		return problem("failed_precondition", "massd cannot account for the original Session owner")
	}
	ref = resource.SessionRef{SessionID: found.Status.SessionID}
	var targetRuntime resource.Runtime
	if err := c.GetRuntime(ctx, b.RuntimeRef, &targetRuntime); err != nil {
		return problem("failed_precondition", "cannot verify the original Runtime type")
	}
	var method string
	var reusable bool
	switch {
	case r.StopRuntime:
		if b.RuntimeUID != "" && (targetRuntime.Metadata.UID != b.RuntimeUID || found.Status.State == "offline" && targetRuntime.Metadata.ResourceVersion != b.RuntimeVersion) {
			return problem("failed_precondition", "current Runtime cannot prove ownership of the original Session")
		}
		if found.Status.State == "offline" && b.RuntimeVersion == "" {
			return problem("failed_precondition", "offline snapshot has no matching Runtime ownership evidence")
		}
		if err := stopOwnedRuntime(ctx, c, targetRuntime); err != nil {
			return err
		}
		method = "runtime_stopped"
	case targetRuntime.Spec.Type == "claude" && (goruntime.GOOS == "darwin" || goruntime.GOOS == "linux"):
		// Only an online applied Session proves there is a native process handle
		// to close. The Claude local driver waits for native process cleanup.
		if !sessionOwnsClose(found) {
			return problem("failed_precondition", "cannot confirm Claude owns a native process for this Session")
		}
		if _, err := closeOriginalSession(ctx, c, b, spec, ref); err != nil {
			return err
		}
		method, reusable = "native_session_closed", true
	default:
		return problem("failed_precondition", "Session close alone does not establish Runtime process release; opt in to stopping the shared local Runtime")
	}
	if err := c.GetSession(ctx, ref, &found); err != nil || found.Status.State != "offline" || !bindingMatches(b, spec, found) {
		return problem("failed_precondition", "original Session identity was not verified offline after release")
	}
	r.SessionID, r.SessionUID = found.Status.SessionID, found.Metadata.UID
	r.ClosedResource = found.Metadata.ResourceVersion
	r.Close = &BindingClose{At: now(), ResourceVersion: r.ClosedResource, Method: method, Reusable: reusable}
	if r.StopRuntime {
		r.RelatedReleases = s.observeRuntimeReleases(ctx, c, targetRuntime, b.ID)
	}
	return nil
}

func stopOwnedRuntime(ctx context.Context, c *client.Client, rt resource.Runtime) error {
	if (rt.Spec.Type != "codex" && rt.Spec.Type != "claude") ||
		(rt.Status.State != "running" && rt.Status.State != "stopping") {
		return problem("failed_precondition", "original local Runtime is not verified running or stopping")
	}
	var stopped resource.Runtime
	if err := c.StopRuntime(ctx, rt.Metadata.Name, &stopped); err != nil ||
		stopped.Status.State != "stopped" || stopped.Metadata.UID != rt.Metadata.UID {
		return problem("failed_precondition", "original local Runtime did not settle its stop")
	}
	var observed resource.Runtime
	if err := c.GetRuntime(ctx, rt.Metadata.Name, &observed); err != nil || observed.Status.State != "stopped" ||
		observed.Metadata.UID != stopped.Metadata.UID || observed.Metadata.ResourceVersion != stopped.Metadata.ResourceVersion {
		return problem("failed_precondition", "original local Runtime stop did not survive readback")
	}
	return nil
}

func (s *Store) saveRecoveryFailure(ctx context.Context, r Recovery) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE session_bindings SET body=json_set(body,'$.recovery',json(?))
 WHERE id=? AND json_extract(body,'$.recovery.id')=? AND json_extract(body,'$.recovery.status')='reconciling'`, raw, r.SessionBindingID, r.ID)
	return err
}

func (s *Store) completeRecovery(ctx context.Context, b SessionBinding, r Recovery) (Recovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var current SessionBinding
	if err := readJSON(ctx, tx, "SELECT body FROM session_bindings WHERE id=?", &current, b.ID); err != nil {
		return r, err
	}
	if current.State != b.State || current.State != "revoked" && current.State != "recovering" {
		return r, problem("stale_binding", "binding changed during recovery")
	}
	if current.Recovery == nil || current.Recovery.ID != r.ID {
		return r, problem("stale_binding", "binding has a newer recovery operation")
	}
	b = current
	if r.Mode != "retry" {
		b.Close = r.Close
		if r.SessionID != "" {
			b.SessionID, b.SessionUID = r.SessionID, r.SessionUID
		}
		if r.Mode == "replace" {
			release := *b.Close
			release.Reusable = false
			b.Close, r.Close = &release, &release
		}
		if err := putJSON(ctx, tx, "UPDATE session_bindings SET body=?2 WHERE id=?1", b, b.ID); err != nil {
			return r, err
		}
		if err := saveRelatedReleases(ctx, tx, r.RelatedReleases); err != nil {
			return r, err
		}
		if b.Role == "issue" {
			if b.State != "revoked" {
				if err := s.replaceIssue(ctx, tx, b, &r); err != nil {
					return r, err
				}
			}
		} else if err := s.replaceBinding(ctx, tx, b, &r); err != nil {
			return r, err
		}
	} else {
		b.State, b.LastError, b.NextAttemptAt = "ready", "", ""
		b.SessionID, b.SessionUID = r.SessionID, r.SessionUID
		if err := putJSON(ctx, tx, "UPDATE session_bindings SET state=?2,next_attempt_at='',body=?3 WHERE id=?1", b, b.ID, b.State); err != nil {
			return r, err
		}
	}
	r.Status, r.LastError, r.CompletedAt = "completed", "", now()
	if err := putJSON(ctx, tx, "UPDATE session_bindings SET body=json_set(body,'$.recovery',json(?2)) WHERE id=?1", r, b.ID); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *Store) replaceBinding(ctx context.Context, tx *sql.Tx, b SessionBinding, r *Recovery) error {
	if b.Role == "topic" {
		var t Topic
		if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &t, b.OwnerID); err != nil {
			return err
		}
		if t.SessionBindingID != b.ID {
			return problem("stale_binding", "Topic has a newer binding")
		}
	}
	if err := revokeBinding(ctx, tx, b.ID); err != nil {
		return err
	}
	at := now()
	next, err := createRoleBinding(ctx, tx, s.config, b.Role, b.OwnerID, b.Workspace, at, b.Generation+1, b)
	if err != nil {
		return err
	}
	r.ReplacementSessionBindingID = next.ID
	if b.Role == "topic" {
		if _, err := tx.ExecContext(ctx, "UPDATE topics SET body=json_set(body,'$.sessionBindingId',?) WHERE id=?", next.ID, b.OwnerID); err != nil {
			return storageError()
		}
	}
	recipient := eventIdentity{Kind: b.Role, TopicID: b.OwnerID}
	kind := "binding_recovered"
	if b.Role == "topic" {
		var t Topic
		if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &t, b.OwnerID); err != nil {
			return err
		}
		if terminal(t.Status) {
			kind = contextSupplement
		}
	}
	payload, err := json.Marshal(map[string]string{"previousSessionBindingId": b.ID, "recovery": "binding_recovered"})
	if err != nil {
		return storageError()
	}
	return enqueueInternalEvent(ctx, tx, internalEvent{
		ID: newID("evt"), Kind: kind, Sender: eventIdentity{Kind: "core"}, Recipient: recipient,
		Body: payload,
	})
}

func (s *Store) replaceUnrecoverableBinding(ctx context.Context, b SessionBinding, session resource.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var current SessionBinding
	if err := readJSON(ctx, tx, `SELECT body FROM session_bindings WHERE id=? AND state='pending'`, &current, b.ID); err != nil {
		return err
	}
	router := current.Role == "router" && current.RuntimeType == "claude"
	if (!router && current.Role != "issue") || !sameBinding(current, b) || current.PreviousOwnerUnknown ||
		current.SessionID == "" || current.SessionUID == "" ||
		current.SessionID != session.Status.SessionID || current.SessionUID != session.Metadata.UID ||
		!resumeRecoveryMissing(session) {
		return problem("stale_binding", "Session recovery evidence no longer matches the binding")
	}
	if router {
		// Router events are addressed to the role across generations. The
		// ordinary dispatcher may retry dispatching events for the same owner.
		// Transferring those to a fresh Session requires explicit reconciliation.
		var outstanding bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM internal_events
 WHERE `+eventRecipientKind+`='router' AND `+eventRecipientOwner+`=? AND status='dispatching')`,
			current.eventRecipientID()).Scan(&outstanding)
		if err != nil {
			return storageError()
		}
		if outstanding {
			return problem("router_input_outcome_unknown", "An in-flight Router input requires reconciliation before replacement")
		}
	}
	settlement := BindingClose{At: now(), ResourceVersion: session.Metadata.ResourceVersion, Method: resource.SessionReasonRecoveryMissing}
	if err := putJSON(ctx, tx, "UPDATE session_bindings SET body=json_set(body,'$.close',json(?2)) WHERE id=?1", settlement, current.ID); err != nil {
		return err
	}
	if router {
		current.Close = &settlement
		err = s.replaceBinding(ctx, tx, current, &Recovery{})
	} else {
		err = s.replaceIssue(ctx, tx, current, &Recovery{})
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) replaceIssue(ctx context.Context, tx *sql.Tx, b SessionBinding, r *Recovery) error {
	issue, _, err := currentBoundIssue(ctx, tx, b)
	if err != nil {
		return err
	}
	at := now()
	if _, err := retireIssueBinding(ctx, tx, issue, at, false); err != nil {
		return err
	}
	issue.CurrentSessionBindingID = ""
	if !canContinueIssue(issue) {
		issue.Revision++
		issue.UpdatedAt = at
		return saveIssue(ctx, tx, issue)
	}
	next, err := s.scheduleIssue(ctx, tx, issue, eventIdentity{Kind: "core"})
	if err != nil {
		return err
	}
	r.ReplacementSessionBindingID = next.SessionBinding.ID
	return nil
}
