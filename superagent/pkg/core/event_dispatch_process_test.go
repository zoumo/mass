package core_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testProducerToken = "test-internal-event-producer"

func eventFixture(t *testing.T, endpoint string) (*fixture, string) {
	t.Helper()
	f := fixtureAt(t)
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	token := filepath.Join(f.dir, "operator.token")
	write(t, token, strings.Repeat("O", 48))
	cwd, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	write(t, f.config, fixedListen(t, string(saved))+"massd:\n  endpoint: "+endpoint+"\noperatorTokenFile: "+token+"\nsessions:\n  routerWorkspace: "+cwd+"\n"+fixtureExecutionProfiles)
	return f, token
}

func readRecord[T any](t *testing.T, p *process, path string) T {
	t.Helper()
	status, data := request(t, http.MethodGet, p.endpoint, "/v1/core/"+path, "", nil)
	require.Equal(t, http.StatusOK, status, string(data))
	var out T
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func fixedListen(t *testing.T, config string) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return strings.Replace(config, "127.0.0.1:0", address, 1)
}
