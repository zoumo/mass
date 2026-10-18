package claude

import (
	"errors"
	sdk "github.com/zoumo/agent-sdk-go/claude"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func submissionError(err error) (rt.Submission, error) {
	r := rt.UnknownSubmission()
	r.HandedToSDK = rt.Known(true)
	r.NativeAcknowledged = rt.Observation{State: "not_supported"}
	r.Outcome = "unknown"
	category := "outcome_unknown"
	var native *sdk.Error
	var control *sdk.ControlError
	if errors.As(err, &native) && !native.OutcomeUnknown {
		r.WriteCompleted = rt.Known(false)
		r.Outcome = "rejected"
		category = "unavailable"
	}
	if errors.As(err, &control) {
		r.Outcome = "rejected"
		category = "native_rejected"
	}
	e := &rt.SafeError{Category: category, Scope: "operation", Message: "Claude command did not settle", Recovery: "inspect original operation; do not automatically resend"}
	r.Failure = e
	return r, e
}
