package core

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const (
	batchWindow  = 200 * time.Millisecond
	batchLimit   = 20
	batchBytes   = 64 * 1024
	previewBytes = 8 * 1024
)

type Attempt struct {
	ID             string
	Status         string
	ErrorCode      string
	ErrorReason    string
	ObservationGap string
}

func classifyAttempt(a Attempt, acceptance runtime.Acceptance, result string, isPrompt bool, err error) Attempt {
	a.Status = "blocked"
	var op *runtime.OperationError
	var rpc *rpcwire.RPCError
	switch {
	case workResponseUnconfirmed(err):
		a.Status, a.ErrorCode, a.ObservationGap = "transport_unknown", "transport_unknown", "massd_receipt_unconfirmed"
	case errors.As(err, &op):
		a.ErrorCode, a.ErrorReason = op.Code, op.Reason
		switch {
		case op.Code == "outcome_unknown" || op.Reason == "input_outcome_unknown":
			a.Status, a.ObservationGap = "blocked_unknown", "native_input_outcome_unknown"
		case op.Reason == "input_in_flight" || op.Reason == "session_resume_required" || op.Reason == "session_delivery_overloaded":
			a.Status = "pending"
		case op.Code == "unavailable" || op.Code == "resource_exhausted" || op.Code == "deadline_exceeded":
			if acceptance.Sent != nil && !*acceptance.Sent || acceptance.NativeAccepted != nil && !*acceptance.NativeAccepted {
				a.Status = "pending"
			} else {
				a.Status, a.ObservationGap = "blocked_unknown", "missing_submission_evidence"
			}
		}
	case errors.As(err, &rpc):
		a.ErrorCode, a.ErrorReason = "massd_rpc_error", strconv.FormatInt(rpc.Code, 10)
		if rpc.Code == -32603 {
			a.Status, a.ObservationGap = "blocked_unknown", "massd_internal_outcome_unknown"
		}
	case err != nil:
		a.Status, a.ErrorCode, a.ObservationGap = "transport_unknown", "transport_unknown", "massd_response_missing"
	case result == "not_applied":
		a.ErrorCode = "not_applied"
	case acceptance.NativeAccepted != nil && !*acceptance.NativeAccepted:
		a.ErrorCode = "native_rejected"
	case acceptance.Sent != nil && *acceptance.Sent &&
		(!isPrompt || result == "applied_new_work" || result == "applied_current_work" || result == "applied_best_effort"):
		a.Status = "delivered"
	default:
		a.ErrorCode = "input_not_confirmed"
		a.ObservationGap = "missing_submission_evidence"
	}
	return a
}

func workResponseUnconfirmed(err error) bool {
	var op *runtime.OperationError
	return errors.As(err, &op) && op.Code == "outcome_unknown" && op.Reason == "operation_unconfirmed"
}

func permanentHandshakeRejection(err error) bool {
	// The MASS client reports HTTP upgrade failures as text with the response status.
	status, ok := strings.CutPrefix(err.Error(), "WebSocket handshake failed: ")
	if !ok {
		return false
	}
	code, _, _ := strings.Cut(status, " ")
	statusCode, parseErr := strconv.Atoi(code)
	return parseErr == nil && statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError && statusCode != http.StatusTooManyRequests
}

func (s *Server) dispatchSessionInput(ctx context.Context, b SessionBinding, spec resource.SessionSpec, a Attempt,
	method string, wire []byte, admit func(*issueWatch) (bool, error),
) (Attempt, *issueWatch) {
	c, err := s.massClient()
	if err != nil {
		a.Status, a.ErrorCode = "blocked", "massd_configuration_invalid"
		return a, nil
	}
	defer func() { _ = c.Close() }()
	if err = c.Initialize(ctx, nil, nil); err != nil {
		a.Status, a.ErrorCode = "pending", "massd_connect_unavailable"
		var op *runtime.OperationError
		if permanentHandshakeRejection(err) {
			a.Status, a.ErrorCode = "blocked", "massd_handshake_rejected"
		} else if errors.As(err, &op) && (op.Code == "unauthenticated" || op.Code == "permission_denied" || op.Code == "unsupported" || op.Code == "invalid_argument") {
			a.Status, a.ErrorCode, a.ErrorReason = "blocked", op.Code, op.Reason
		}
		return a, nil
	}
	ref := resource.SessionRef{SessionID: b.SessionID}
	var session resource.Session
	err = c.GetSession(ctx, ref, &session)
	switch {
	case b.Role == "issue" && errorCode(err) == "not_found":
		a.Status, a.ErrorCode, a.ErrorReason = "pending", "not_found", "session_resume_required"
		return a, nil
	case errorCode(err) == "not_found" || err == nil && !bindingMatches(b, spec, session):
		a.Status, a.ErrorCode = "blocked", "session_configuration_conflict"
		return a, nil
	case err != nil:
		a.Status, a.ErrorCode = "pending", "massd_session_read_unavailable"
		return a, nil
	case session.Status.State == "offline" || session.Status.State == "closing" || session.Status.State == "opening" ||
		(b.Role == "issue" && ownershipUnconfirmed(session)):
		a.Status, a.ErrorCode, a.ErrorReason = "pending", "conflict", "session_resume_required"
		return a, nil
	}
	var watch *issueWatch
	switch b.Role {
	case "issue":
		commitGate("issue_before_rpc")
		if method == "start_or_steer" {
			watch = s.prepareIssueWatch(ctx, b, a)
		}
	case "topic":
		commitGate("topic_before_rpc")
	}
	armed, err := admit(watch)
	if err != nil {
		watch.close()
		a.Status, a.ErrorCode = "pending", "dispatch_no_longer_admitted"
		return a, nil
	}
	if watch != nil {
		if armed {
			s.stopIssueWatch(b.ID)
		} else {
			watch.close()
			watch = nil
		}
	}
	input := runtime.Input{Content: []events.ContentBlock{events.Text(string(wire))}}
	if method == "inject" {
		var receipt runtime.InjectReceipt
		err := c.Inject(ctx, ref, input, &receipt)
		return classifyAttempt(a, receipt.Acceptance, "", false, err), nil
	}
	var receipt runtime.InputReceipt
	if watch != nil {
		watch.dispatchedAt = time.Now()
	}
	err = c.SendPrompt(ctx, ref, input, &receipt)
	a = classifyAttempt(a, receipt.Acceptance, string(receipt.Result), true, err)
	if watch != nil {
		watch.recordReceipt(a, receipt.Result, receipt.Submission)
	}
	return a, watch
}
