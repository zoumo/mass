package runtime

import "errors"

// These classifications originate at the known rejection site, never by parsing
// a native diagnostic. Unclassified I/O and native failures remain uncertain.
var (
	ErrInvalidArgument    = errors.New("invalid_argument")
	ErrFailedPrecondition = errors.New("failed_precondition")
)

func InvalidArgument(message string) error {
	return errors.Join(ErrInvalidArgument, errors.New(message))
}

func FailedPrecondition(message string) error {
	return errors.Join(ErrFailedPrecondition, errors.New(message))
}

// OperationError is the shared public error value, independent of HTTP mapping.
type OperationError struct {
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *OperationError) Error() string { return e.Code + ": " + e.Reason }
func Unsupported() error {
	return &OperationError{"unsupported", "unsupported_content", "Input content is not supported by this runtime"}
}

func Unavailable(reason string) error {
	return &OperationError{"unavailable", reason, "Session observation is unavailable"}
}

func Exhausted(reason string) error {
	return &OperationError{"resource_exhausted", reason, "Observation capacity exceeded"}
}
