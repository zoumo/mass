package resource_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

func TestHistoryLoadEvidenceRetainsFalse(t *testing.T) {
	raw, err := json.Marshal(resource.HistoryLoadStatus{LoadID: "load-a", Phase: "accepted"})
	require.NoError(t, err)
	require.JSONEq(t, `{"loadId":"load-a","phase":"accepted","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}`, string(raw))
}
