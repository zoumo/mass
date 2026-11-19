package client_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInjectClientInvalidInputIsKnownLocalRejection(t *testing.T) {
	c, err := client.New("invalid-url", "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	defer c.Close()
	err = c.Inject(t.Context(), resource.SessionRef{SessionID: "native"}, runtime.Input{}, nil)
	var p *runtime.OperationError
	require.ErrorAs(t, err, &p)
	require.Equal(t, "invalid_argument", p.Code)
}
