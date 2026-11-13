package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestIllegalNativeIdentityNeverBecomesPersistentSession(t *testing.T) {
	for _, id := range []string{".", ".."} {
		t.Run(id, func(t *testing.T) {
			svc, _, _ := lifecycleService(t, t.TempDir())
			path, work := identityOnlyScenario(t, id)
			var err error
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			_, err = svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, "invalid_session_id", public.Reason)
			page, err := svc.ListSessions(resource.SessionQuery{})
			require.NoError(t, err)
			require.Empty(t, page.Items)
			_, err = svc.StopRuntime(ctx(t), "agent")
			require.NoError(t, err, "invalid open resource and reservation must be released")
		})
	}
}

// Retain only the handshake/open and unsubscribe/closed exchanges. The strict
// SDK peer rejects any implicit input between creation and explicit close.
func identityOnlyScenario(t *testing.T, id string) (string, string) {
	t.Helper()
	path, work := scenario(t)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture map[string]any
	require.NoError(t, json.Unmarshal(b, &fixture))
	frames, ok := fixture["frames"].([]any)
	require.True(t, ok)
	turnStart, unsubscribe := -1, -1
	for index, value := range frames {
		frame, _ := value.(map[string]any)
		wire, _ := frame["wire"].(map[string]any)
		method, _ := wire["method"].(string)
		switch method {
		case "turn/start":
			if turnStart < 0 {
				turnStart = index
			}
		case "thread/unsubscribe":
			unsubscribe = index
		}
	}
	require.Positive(t, turnStart)
	require.Greater(t, unsubscribe, turnStart)
	fixture["frames"] = append(frames[:turnStart], frames[unsubscribe:]...)
	b, err = json.Marshal(fixture)
	require.NoError(t, err)
	b = bytes.ReplaceAll(b, []byte("codex-session"), []byte(id))
	path = filepath.Join(t.TempDir(), "invalid-id.json")
	require.NoError(t, os.WriteFile(path, b, 0o600))
	return path, work
}

func TestRPCCreateWaitsForIdentityAndReturnsRawID(t *testing.T) {
	c := ctx(t)
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	gate := &identityGate{MetaStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	svc := service.New(gate, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}}))
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))) })
	path, work := identityOnlyScenario(t, "a/b %空间")
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(c, "agent")
	require.NoError(t, err)
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{Token: "test-token"}))
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	type result struct {
		session resource.SessionCreated
		err     error
	}
	rpcClient := newHTTPClient(t, server.URL, "test-token")
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
	}()
	done := make(chan result, 1)
	go func() {
		var created resource.SessionCreated
		err := rpcClient.CreateSession(c, resource.Session{
			Metadata: resource.Metadata{Namespace: "one", Name: "demo"},
			Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}},
		}, &created)
		done <- result{session: created, err: err}
	}()
	select {
	case <-gate.entered:
	case <-c.Done():
		t.Fatal(c.Err())
	}
	select {
	case <-done:
		t.Fatal("creation returned before identity commit")
	case <-time.After(30 * time.Millisecond):
	}
	_, err = svc.GetSession(resource.SessionRef{Namespace: "one", Name: "demo"})
	require.Error(t, err)
	close(gate.release)
	released = true
	var out result
	select {
	case out = <-done:
	case <-c.Done():
		t.Fatal(c.Err())
	}
	require.NoError(t, out.err)
	require.Equal(t, "a/b %空间", out.session.Status.SessionID)
	for _, ref := range []resource.SessionRef{
		{SessionID: out.session.Status.SessionID},
		{Namespace: "one", Name: "demo"},
	} {
		var found resource.Session
		require.NoError(t, rpcClient.GetSession(c, ref, &found))
		require.Equal(t, "a/b %空间", found.Status.SessionID)
	}
	require.NoError(t, rpcClient.CloseSession(c, resource.SessionRef{Namespace: "one", Name: "demo"}, nil))
}
