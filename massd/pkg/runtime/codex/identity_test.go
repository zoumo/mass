package codex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSameNativeCreatedAt(t *testing.T) {
	require.True(t, sameNativeCreatedAt(100, 100))
	require.True(t, sameNativeCreatedAt(100, 101))
	require.True(t, sameNativeCreatedAt(101, 100))
	require.False(t, sameNativeCreatedAt(100, 102))
	require.False(t, sameNativeCreatedAt(102, 100))
	require.False(t, sameNativeCreatedAt(0, 0))
	require.False(t, sameNativeCreatedAt(-1, 0))
}
