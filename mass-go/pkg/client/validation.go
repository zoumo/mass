package client

import (
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func invalid() *runtime.OperationError {
	return &runtime.OperationError{Code: "invalid_argument", Reason: "invalid_request", Message: "Invalid request"}
}

func validSegment(v string) bool { return v != "" && v != "." && v != ".." && utf8.ValidString(v) }
