package claude

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type configSDKSession struct {
	agent.Session
	receipt agent.ConfigReceipt
	err     error
	calls   int
}

func (s *configSDKSession) SetConfig(_ context.Context, change agent.ConfigChange) (agent.ConfigReceipt, error) {
	s.calls++
	r := s.receipt
	r.Change = change
	return r, s.err
}
func (s *configSDKSession) Events() (agent.EventStream, error) { return s, nil }
func (*configSDKSession) Next(ctx context.Context) (agent.Event, error) {
	<-ctx.Done()
	return agent.Event{}, ctx.Err()
}
func (*configSDKSession) Err() error { return nil }

func TestLiveSessionConfigurationSDKReceipt(t *testing.T) {
	sdkFailure := func(code agent.ErrorCode, reason string, cause error) error {
		return fmt.Errorf("SDK configuration call: %w", &agent.Error{Code: code, Reason: reason, Message: "safe", Cause: cause})
	}
	cases := []struct {
		name       string
		acceptance agent.Acceptance
		err        error
		code       string
	}{
		{"missing", agent.Acceptance{}, nil, ""},
		{"false", agent.Acceptance{Sent: runtime.Bool(false), NativeAccepted: runtime.Bool(false), NativeRecorded: runtime.Bool(false)}, nil, ""},
		{"true", agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes(), NativeRecorded: runtime.Yes()}, nil, ""},
		{"not_sent", agent.Acceptance{Sent: runtime.Bool(false)}, &agent.Error{Code: agent.ErrorCode("deadline_exceeded"), Reason: "sdk_not_sent", Message: "safe"}, "deadline_exceeded"},
		{"rejected", agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)}, &agent.Error{Code: agent.ErrorNativeRejected, Reason: "sdk_rejection", Message: "safe"}, "native_rejected"},
		{"unknown", agent.Acceptance{Sent: runtime.Yes()}, &agent.Error{Code: agent.ErrorOutcomeUnknown, Reason: "sdk_unknown", Message: "safe"}, "outcome_unknown"},
		{"unsupported", agent.Acceptance{Sent: runtime.Bool(false)}, &agent.Error{Code: agent.ErrorUnsupported, Reason: "sdk_unsupported", Message: "safe"}, "unsupported"},
		{"closed", agent.Acceptance{Sent: runtime.Bool(false)}, &agent.Error{Code: agent.ErrorClosed, Reason: "session_closed", Message: "safe"}, "conflict"},
		{"unclassified", agent.Acceptance{Sent: runtime.Yes()}, errors.New("SECRET"), "outcome_unknown"},
		{"not_sent_sdk_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", context.DeadlineExceeded), "deadline_exceeded"},
		{"sent_sdk_deadline", agent.Acceptance{Sent: runtime.Yes()}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", context.DeadlineExceeded), "unavailable"},
		{"missing_sent_sdk_deadline", agent.Acceptance{}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", context.DeadlineExceeded), "unavailable"},
		{"not_sent_canceled", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", context.Canceled), "unavailable"},
		{"not_sent_other_cause", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", errors.New("transport unavailable")), "unavailable"},
		{"not_sent_deadline_string", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnavailable, "set_config_failed", errors.New(context.DeadlineExceeded.Error())), "unavailable"},
		{"not_sent_other_reason", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnavailable, "other_operation_failed", context.DeadlineExceeded), "unavailable"},
		{"not_sent_rejected_deadline", agent.Acceptance{Sent: runtime.Bool(false), NativeAccepted: runtime.Bool(false)}, sdkFailure(agent.ErrorNativeRejected, "sdk_rejection", context.DeadlineExceeded), "native_rejected"},
		{"not_sent_unsupported_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorUnsupported, "sdk_unsupported", context.DeadlineExceeded), "unsupported"},
		{"not_sent_closed_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorClosed, "session_closed", context.DeadlineExceeded), "conflict"},
		{"not_sent_precondition_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorFailedPrecondition, "precondition", context.DeadlineExceeded), "conflict"},
		{"not_sent_unknown_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, sdkFailure(agent.ErrorOutcomeUnknown, "sdk_unknown", context.DeadlineExceeded), "outcome_unknown"},
		{"not_sent_unclassified_deadline", agent.Acceptance{Sent: runtime.Bool(false)}, fmt.Errorf("unclassified: %w", context.DeadlineExceeded), "outcome_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			native := &configSDKSession{receipt: agent.ConfigReceipt{Acceptance: tc.acceptance}, err: tc.err}
			driver := &Driver{sessions: map[string]*session{"native": {native: native}}}

			var optional runtime.SessionConfigDriver = driver
			change := agent.ConfigChange{ID: agent.ConfigModel, Value: " Native/Model "}
			callCtx := t.Context()
			if tc.name == "not_sent_other_cause" {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithTimeout(callCtx, 0)
				defer cancel()
				require.ErrorIs(t, callCtx.Err(), context.DeadlineExceeded)
			}
			receipt, err := optional.SetSessionConfig(callCtx, "native", change)
			require.Equal(t, change, receipt.Change)
			require.Equal(t, tc.acceptance, receipt.Acceptance)
			require.Same(t, tc.acceptance.Sent, receipt.Acceptance.Sent)
			require.Same(t, tc.acceptance.NativeAccepted, receipt.Acceptance.NativeAccepted)
			require.Same(t, tc.acceptance.NativeRecorded, receipt.Acceptance.NativeRecorded)
			require.Equal(t, 1, native.calls)
			if tc.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
				var public *runtime.OperationError
				require.ErrorAs(t, err, &public)
				require.Equal(t, tc.code, public.Code)
				require.NotContains(t, public.Message, "SECRET")
				var sdk *agent.Error
				if errors.As(tc.err, &sdk) {
					require.Equal(t, sdk.Reason, public.Reason)
					var retained *agent.Error
					require.ErrorAs(t, err, &retained)
					require.Same(t, sdk, retained)
					if sdk.Cause != nil {
						require.ErrorIs(t, err, sdk.Cause)
					}
				}
			}
			// The optional SDK controller is not implied by the base Session interface.
			driver.sessions["native"].native = struct{ agent.Session }{native}
			receipt, err = optional.SetSessionConfig(t.Context(), "native", change)
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, "unsupported", public.Code)
			require.Equal(t, change, receipt.Change)
			require.Equal(t, runtime.Bool(false), receipt.Acceptance.Sent)
			require.Equal(t, 1, native.calls)
		})
	}
}
