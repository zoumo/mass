package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type Error = runtime.OperationError

func Invalid() *Error {
	return &Error{Code: "invalid_argument", Reason: "invalid_request", Message: "Invalid request"}
}

// PublicError classifies only explicit sentinels. Native diagnostics never escape.
func PublicError(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, runtime.ErrResourcesReleased):
		return &Error{Code: "conflict", Reason: "native_open_failed", Message: "Native open failed; owned resources released"}
	case errors.Is(err, runtime.ErrInvalidArgument):
		return Invalid()
	case errors.Is(err, runtime.ErrFailedPrecondition):
		return &Error{Code: "conflict", Reason: "operation_rejected", Message: "Operation rejected by current resource state"}
	case errors.Is(err, store.ErrNotFound):
		return &Error{Code: "not_found", Reason: "resource_not_found", Message: "Resource not found"}
	case errors.Is(err, store.ErrConflict):
		return &Error{Code: "conflict", Reason: "resource_conflict", Message: "Resource conflict"}
	case errors.Is(err, store.ErrReadOnly):
		return &Error{Code: "unavailable", Reason: "read_only", Message: "Store is read only"}
	default:
		return &Error{Code: "outcome_unknown", Reason: "operation_unconfirmed", Message: "Operation result unconfirmed; inspect state before further action"}
	}
}

func statusFor(code string) int {
	switch code {
	case "invalid_argument":
		return 400
	case "unauthenticated":
		return 401
	case "permission_denied":
		return 403
	case "not_found":
		return 404
	case "already_exists", "conflict":
		return 409
	case "unsupported":
		return 422
	case "resource_exhausted":
		return 429
	case "unavailable":
		return 503
	case "deadline_exceeded":
		return 504
	default:
		return 500
	}
}

func fail(w http.ResponseWriter, status int, err *Error) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	w.Header().Del("Location")
	// Do not let net/http drain an unread body before sending an error.
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Type", "application/json")
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(status)
	if encodeErr := json.NewEncoder(w).Encode(err); encodeErr != nil {
		return // The response is already committed; the client may have disconnected.
	}
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		public := PublicError(err)
		var protocol *protocolError
		if errors.As(err, &protocol) {
			fail(w, protocol.status, protocol.public)
			return
		}
		fail(w, statusFor(public.Code), public)
		return
	}
	if status == 204 {
		w.WriteHeader(status)
		return
	}
	if err := validateOutput(reflect.ValueOf(value), 0); err != nil {
		fail(w, 500, &Error{Code: "internal", Reason: "encoding_failed", Message: "Response could not be encoded"})
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		fail(w, 500, &Error{Code: "internal", Reason: "encoding_failed", Message: "Response could not be encoded"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

type protocolError struct {
	status int
	public *Error
}

func (e *protocolError) Error() string { return e.public.Error() }
