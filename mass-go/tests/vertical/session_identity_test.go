package vertical_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer"
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
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": path}}})
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
// SDK peer rejects any implicit input between creation and explicit archive.
func identityOnlyScenario(t *testing.T, id string) (string, string) {
	t.Helper()
	work := t.TempDir()
	b, err := os.ReadFile("testdata/codex.json")
	require.NoError(t, err)
	quoted, err := json.Marshal(work)
	require.NoError(t, err)
	b = bytes.ReplaceAll(b, []byte(`"/synthetic"`), quoted)
	var fixture struct {
		Version    int               `json:"version"`
		Provenance string            `json:"provenance"`
		Source     json.RawMessage   `json:"source"`
		TimeoutMS  int               `json:"timeoutMs"`
		Frames     []json.RawMessage `json:"frames"`
	}
	require.NoError(t, json.Unmarshal(b, &fixture))
	fixture.Frames = append(fixture.Frames[:5], fixture.Frames[15:]...)
	b, err = json.Marshal(fixture)
	require.NoError(t, err)
	b = bytes.ReplaceAll(b, []byte("codex-session"), []byte(id))
	material, err := testpeer.Author(t.TempDir(), "codex", b)
	require.NoError(t, err)
	launcher, err := testpeer.Direct(t.TempDir(), "codex", material.Recording)
	require.NoError(t, err)
	return launcher.Path, work
}

func TestHTTPCreateWaitsForIdentityAndReturnsRawIDLocation(t *testing.T) {
	c := ctx(t)
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	gate := &identityGate{MetaStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	svc := service.New(gate, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}}))
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))) })
	path, work := identityOnlyScenario(t, "a/b %空间")
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(c, "agent")
	require.NoError(t, err)
	h, err := transport.New(svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	body, err := json.Marshal(map[string]any{"metadata": map[string]string{"namespace": "one", "name": "demo"}, "spec": map[string]string{"runtimeRef": "agent", "cwd": work}})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(c, http.MethodPost, server.URL+"/apis/v1/sessions", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	type result struct {
		response *http.Response
		err      error
	}
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
	}()
	done := make(chan result, 1)
	go func() {
		res, e := http.DefaultClient.Do(req) //nolint:bodyclose // Ownership transfers to the receiving test, which closes both early and committed responses.
		done <- result{res, e}
	}()
	select {
	case <-gate.entered:
	case <-c.Done():
		t.Fatal(c.Err())
	}
	select {
	case early := <-done:
		if early.response != nil {
			early.response.Body.Close()
		}
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
	raw, err := io.ReadAll(out.response.Body)
	out.response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, 201, out.response.StatusCode, string(raw))
	require.Equal(t, "/apis/v1/sessions/a%2Fb%20%25%E7%A9%BA%E9%97%B4", out.response.Header.Get("Location"))
	for _, target := range []string{out.response.Header.Get("Location"), "/apis/v1/namespaces/one/sessions/a%2Fb%20%25%E7%A9%BA%E9%97%B4", "/apis/v1/namespaces/one/sessions/demo"} {
		request, err := http.NewRequestWithContext(c, http.MethodGet, server.URL+target, http.NoBody)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer test-token")
		res, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode, string(b))
		require.Contains(t, string(b), `"sessionId":"a/b %空间"`)
	}
	req, err = http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/apis/v1/namespaces/one/sessions/demo/archive", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode, string(b))
}
