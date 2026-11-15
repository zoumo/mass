package sa

import "errors"

// OperationError is the public, transport-neutral error value used by mass-sa.
type OperationError struct {
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *OperationError) Error() string {
	return e.Code + ": " + e.Reason + ": " + e.Message
}

func operationError(code, reason, message string) error {
	return &OperationError{Code: code, Reason: reason, Message: message}
}

// IsErrorCode reports whether err contains an OperationError with code.
func IsErrorCode(err error, code string) bool {
	var target *OperationError
	return errors.As(err, &target) && target.Code == code
}
