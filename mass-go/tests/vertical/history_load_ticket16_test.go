package vertical_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTicket16CodexColdDirectInputHTTPHistoryAndContinuedInput(t *testing.T) {
	ticket12ColdLoadHTTP(t, "cold-materialized-direct")
}

func TestTicket16CodexUnchangedDirectInputCapability(t *testing.T) {
	for _, mode := range []string{"cold-direct-equal-null", "cold-direct-equal-false", "cold-direct-equal-true"} {
		t.Run(mode, func(t *testing.T) { ticket12ColdLoadHTTP(t, mode) })
	}
}

func TestTicket16CodexDirectInputMetadataBoundaries(t *testing.T) {
	cases := []ticket12RejectedCase{
		{"cold-materialized-direct:pre:directMissing", 0, 1}, // Omission is not explicit null.
		{"cold-materialized-direct:pre:directFalse", 0, 1},
		{"cold-materialized-direct:pre:directString", 0, 0},
		{"cold-materialized-direct:pre:directNumber", 0, 0},
		{"cold-materialized-direct:resume:directFalse", 0, 1}, // Null -> false is not the observed transition.
		{"cold-materialized-direct:resume:directMissing", 0, 1},
		{"cold-materialized-direct:pre:idle", 0, 1}, // Already loaded: even null -> true is drift.
		{"cold-direct-equal-true:resume:directFalse", 0, 1},
		{"cold-direct-equal-true:resume:directNull", 0, 1},
		{"cold-direct-equal-true:resume:directMissing", 0, 1},
		{"cold-materialized-direct:after:directFalse", 3, 1},
		{"cold-materialized-direct:after:directNull", 3, 1},
		{"cold-materialized-direct:after:directMissing", 3, 1},
		{"cold-materialized-direct:after:directString", 3, 1},
		{"cold-materialized-direct:after:directNumber", 3, 1},
		{"cold:pre:directTrue", 0, 1},             // True -> missing.
		{"cold:resume:directFalse", 0, 1},         // Missing -> false.
		{"loaded:resume:directTrue", 0, 1},        // Loaded -> loaded remains strict.
		{"loaded-direct:resume:directTrue", 0, 1}, // Idle -> idle, with ONLY explicit null -> true drift.
		{"cold-materialized-direct:after:metadata", 3, 1},
		{"cold-materialized-direct:after:statusMetadata", 3, 1},
		{"cold-materialized-direct:after:sessionId", 3, 1},
		{"cold-materialized-direct:after:id", 3, 1},
	}
	for _, field := range []string{
		"active", "unknown", "systemError", "notLoaded", "sessionId", "cwd",
		"modelProvider", "createdAt", "ephemeral", "source", "path", "metadata", "statusMetadata",
		"updatedAtRegression", "environmentUnknown",
	} {
		cases = append(cases, ticket12RejectedCase{"cold-materialized-direct:resume:" + field, 0, 1})
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			calls, counts := ticket12RejectedLoad(t, tc)
			if tc.mode == "loaded-direct:resume:directTrue" {
				var pair []map[string]any
				for _, call := range calls {
					if strings.HasSuffix(call.Method, "/result") {
						pair = append(pair, call.Params["thread"].(map[string]any))
					}
				}
				require.Len(t, pair, 2, "reject before history enumeration")
				require.Contains(t, pair[0], "canAcceptDirectInput")
				require.Nil(t, pair[0]["canAcceptDirectInput"])
				require.Equal(t, true, pair[1]["canAcceptDirectInput"])
				// Assertion-only projection: every other native field is identical.
				delete(pair[0], "canAcceptDirectInput")
				delete(pair[1], "canAcceptDirectInput")
				require.Equal(t, pair[0], pair[1])
				require.Equal(t, "idle", pair[0]["status"].(map[string]any)["type"])
				require.Zero(t, counts["thread/turns/list"])
				require.Zero(t, counts["thread/items/list"])
			}
		})
	}
}
