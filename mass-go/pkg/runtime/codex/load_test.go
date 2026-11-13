package codex

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	sdk "github.com/zoumo/agent-sdk-go/codex"
)

func TestResumeOutcomeUnknownUsesSDKDispatchEvidence(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"classified outcome unknown": {
			err:  &agent.Error{Code: agent.ErrorOutcomeUnknown},
			want: true,
		},
		"dispatched call wrapped by another classification": {
			err: &agent.Error{
				Code:  agent.ErrorInvalidArgument,
				Cause: &sdk.CallError{Dispatch: sdk.OutcomeUnknown},
			},
			want: true,
		},
		"explicit not sent": {
			err:  &sdk.CallError{Dispatch: sdk.NotSent},
			want: false,
		},
		"unclassified": {
			err:  errors.New("local validation failed"),
			want: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, resumeOutcomeUnknown(tc.err))
		})
	}
}
