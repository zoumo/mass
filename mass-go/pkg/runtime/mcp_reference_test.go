package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestLookupMCPEnvironmentReference(t *testing.T) {
	env := map[string]string{
		"TOKEN":   "s3cr3t-value",
		"EMPTY":   "",
		"NUL":     "a\x00b",
		"INVALID": string([]byte{0xff}),
	}
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	value, err := runtime.LookupMCPEnvironmentReference(lookup, "TOKEN")
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", value)

	for _, key := range []string{"MISSING", "EMPTY", "NUL", "INVALID"} {
		_, err := runtime.LookupMCPEnvironmentReference(lookup, key)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "s3cr3t-value")
		require.NotContains(t, err.Error(), "\x00")
	}
}
