package runtime

import "errors"

// These classifications originate at the known rejection site, never by parsing
// a native diagnostic. Unclassified I/O and native failures remain uncertain.
var ErrInvalidArgument = errors.New("invalid_argument")
var ErrFailedPrecondition = errors.New("failed_precondition")

func InvalidArgument(message string) error {
	return errors.Join(ErrInvalidArgument, errors.New(message))
}
func FailedPrecondition(message string) error {
	return errors.Join(ErrFailedPrecondition, errors.New(message))
}
