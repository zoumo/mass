package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeliveryRetryThresholds(t *testing.T) {
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second} {
		delay := retryDelay(i+1, false)
		require.GreaterOrEqual(t, delay, base)
		require.LessOrEqual(t, delay, base+base/5)
	}
	for _, delay := range []time.Duration{retryDelay(7, false), retryDelay(10, true), retryDelay(1, true)} {
		require.GreaterOrEqual(t, delay, 54*time.Second)
		require.LessOrEqual(t, delay, time.Minute)
	}
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := Delivery{Status: "pending", Failures: 9, FirstFailureAt: first.Format(time.RFC3339Nano)}
	require.False(t, needsDeliveryAttention(d, first.Add(10*time.Minute-time.Second)))
	require.True(t, needsDeliveryAttention(d, first.Add(10*time.Minute)))
	d.Failures++
	require.True(t, needsDeliveryAttention(d, first))
	d.Status = "delivered"
	require.False(t, needsDeliveryAttention(d, first.Add(10*time.Minute)))
}
