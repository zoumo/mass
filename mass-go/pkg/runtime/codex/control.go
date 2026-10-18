package codex

import (
	"errors"
	sdk "github.com/zoumo/agent-sdk-go/codex"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func submissionError(err error) (rt.Submission, error) {
	r := rt.UnknownSubmission()
	r.HandedToSDK = rt.Known(true)
	r.Outcome = "unknown"
	category := "outcome_unknown"
	var call *sdk.CallError
	var rpc *sdk.RPCError
	if errors.As(err, &call) && call.Dispatch == sdk.NotSent {
		r.WriteCompleted = rt.Known(false)
		r.Outcome = "rejected"
		category = "unavailable"
	}
	if errors.As(err, &rpc) {
		r.Outcome = "rejected"
		category = "native_rejected"
		r.NativeAcknowledged = rt.Known(true)
	}
	e := &rt.SafeError{Category: category, Scope: "operation", Message: "Codex command did not settle", Recovery: "inspect original operation; do not automatically resend"}
	r.Failure = e
	return r, e
}
