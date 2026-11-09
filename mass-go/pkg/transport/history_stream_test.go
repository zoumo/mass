package transport_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestHistoryAndStreamOpenAPI(t *testing.T) {
	data, err := transport.OpenAPI()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	paths := doc["paths"].(map[string]any)
	for _, base := range []string{"/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		require.Contains(t, paths, base+"/events")
		require.Contains(t, paths, base+"/events/stream")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	union, ok := schemas["SessionEvent"].(map[string]any)
	require.True(t, ok)
	require.Len(t, union["oneOf"], 17)
}
