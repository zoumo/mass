package vertical_test

import "testing"

func TestTicket15CodexColdLoadMaterializesMetadata(t *testing.T) {
	ticket12ColdLoadHTTP(t, "cold-materialized")
}

func TestTicket15CodexColdLoadEmptyEnvironmentAndTimestamp(t *testing.T) {
	for _, mode := range []string{"cold-materialized-empty", "cold-timestamp"} {
		t.Run(mode, func(t *testing.T) { ticket12ColdLoadHTTP(t, mode) })
	}
}

func TestTicket15CodexColdLoadMetadataBoundaries(t *testing.T) {
	for _, tc := range []ticket12RejectedCase{
		{"cold-materialized:pre:updatedAtNegative", 0, 1},
		{"cold-materialized:pre:updatedAtBeforeCreated", 0, 1},
		{"cold-materialized:resume:updatedAtNegative", 0, 1},
		{"cold-materialized:resume:updatedAtRegression", 0, 1},
		{"cold-materialized:pre:environmentsMissing", 0, 1},
		{"cold-materialized:pre:environmentsEmpty", 0, 1},
		{"cold-materialized:resume:environmentsMissing", 0, 1},
		{"cold-materialized:resume:environmentUnknown", 0, 1},
		{"cold-materialized:resume:metadata", 0, 1},
		{"cold-materialized:resume:statusMetadata", 0, 1},
		{"cold-materialized:pre:idle", 0, 1},    // Already loaded: neither field may change.
		{"cold:resume:environmentsEmpty", 0, 1}, // Missing is not explicit null.
		{"loaded:resume:updatedAt", 0, 1},
		{"loaded:resume:environmentsEmpty", 0, 1},
		{"cold-materialized:after:updatedAtAdvance", 3, 1},
		{"cold-materialized:after:environmentsEmpty", 3, 1},
		{"cold-materialized:after:environmentsNull", 3, 1},
		{"cold-materialized:after:environmentUnknown", 3, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) { ticket12RejectedLoad(t, tc) })
	}
}
