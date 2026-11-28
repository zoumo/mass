package trigger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1",
		"http://[::1]:24370/",
		"https://core.example.com",
		"http://192.0.2.10:5970",
		"http://core.example.com",
		"http://core_internal:24370",
	} {
		require.NoError(t, ValidateCoreEndpoint(endpoint), endpoint)
	}
	for _, endpoint := range []string{
		"https://user@core.example.com",
		"https://core.example.com/v1/channel",
		"https://core.example.com?",
		"https://core.example.com#",
		"https://core.example.com:0",
		"https://[core.example.com]",
		"http://2001:db8::10",
		"file://core.example.com",
	} {
		require.Error(t, ValidateCoreEndpoint(endpoint), endpoint)
	}
}

func TestStatusAddress(t *testing.T) {
	require.NoError(t, ValidateStatusEndpoint("http://192.0.2.10:24371"))
	require.NoError(t, ValidateStatusEndpoint("https://trigger.example.com"))
	require.Error(t, ValidateStatusEndpoint("https://trigger.example.com/status"))
	require.NoError(t, ValidateStatusListen("localhost:0"))
	require.NoError(t, ValidateStatusListen("trigger_status:0"))
	require.NoError(t, ValidateStatusListen("0.0.0.0:24371"))
	require.NoError(t, ValidateStatusListen(":24371"))
	require.Error(t, ValidateStatusListen("localhost"))
	require.Error(t, ValidateStatusListen("localhost:65536"))
	require.Error(t, ValidateStatusListen("[localhost]:24371"))
	require.Error(t, ValidateStatusListen("bad/name:24371"))
}
