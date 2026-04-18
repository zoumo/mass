package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// testClientEnv holds a typed client connected to an in-process mock server.
type testClientEnv struct {
	client pkgariapi.Client
	cancel context.CancelFunc
}

// newTestClientEnv starts a jsonrpc.Server with echo-style mock handlers and
// returns a typed ARI client connected to it.
func newTestClientEnv(t *testing.T) *testClientEnv {
	t.Helper()

	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close(); os.Remove(sockPath) })

	srv := jsonrpc.NewServer(slog.Default())
	registerMockHandlers(srv)

	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	c, err := Dial(ctx, sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return &testClientEnv{client: c, cancel: cancel}
}

// registerMockHandlers registers echo-style handlers for all ARI methods.
func registerMockHandlers(srv *jsonrpc.Server) {
	srv.RegisterService("workspace", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": mockMethod(func() any { return &pkgariapi.Workspace{} }, func(v any) any {
				ws := v.(*pkgariapi.Workspace)
				ws.Status.Phase = pkgariapi.WorkspacePhasePending
				return ws
			}),
			"get": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(v any) any {
				key := v.(*pkgariapi.ObjectKey)
				return &pkgariapi.Workspace{Metadata: pkgariapi.ObjectMeta{Name: key.Name}}
			}),
			"list": mockMethod(func() any { return &pkgariapi.ListOptions{} }, func(_ any) any {
				return &pkgariapi.WorkspaceList{Items: []pkgariapi.Workspace{
					{Metadata: pkgariapi.ObjectMeta{Name: "ws1"}},
				}}
			}),
			"delete": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(_ any) any {
				return json.RawMessage(`{}`)
			}),
			"send": mockMethod(func() any { return &pkgariapi.WorkspaceSendParams{} }, func(_ any) any {
				return &pkgariapi.WorkspaceSendResult{Delivered: true}
			}),
		},
	})

	srv.RegisterService("agentrun", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": mockMethod(func() any { return &pkgariapi.AgentRun{} }, func(v any) any {
				ar := v.(*pkgariapi.AgentRun)
				ar.Status.State = "creating"
				return ar
			}),
			"get": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(v any) any {
				key := v.(*pkgariapi.ObjectKey)
				return &pkgariapi.AgentRun{Metadata: pkgariapi.ObjectMeta{Workspace: key.Workspace, Name: key.Name}}
			}),
			"list": mockMethod(func() any { return &pkgariapi.ListOptions{} }, func(_ any) any {
				return &pkgariapi.AgentRunList{Items: []pkgariapi.AgentRun{
					{Metadata: pkgariapi.ObjectMeta{Name: "ar1"}},
				}}
			}),
			"delete": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(_ any) any {
				return json.RawMessage(`{}`)
			}),
			"prompt": mockMethod(func() any { return &pkgariapi.AgentRunPromptParams{} }, func(_ any) any {
				return &pkgariapi.AgentRunPromptResult{Accepted: true}
			}),
			"cancel": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(_ any) any {
				return json.RawMessage(`{}`)
			}),
			"stop": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(_ any) any {
				return json.RawMessage(`{}`)
			}),
			"restart": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(v any) any {
				key := v.(*pkgariapi.ObjectKey)
				return &pkgariapi.AgentRun{Metadata: pkgariapi.ObjectMeta{Workspace: key.Workspace, Name: key.Name}}
			}),
		},
	})

	srv.RegisterService("agent", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": mockMethod(func() any { return &pkgariapi.Agent{} }, func(v any) any {
				return v
			}),
			"get": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(v any) any {
				key := v.(*pkgariapi.ObjectKey)
				return &pkgariapi.Agent{Metadata: pkgariapi.ObjectMeta{Name: key.Name}}
			}),
			"update": mockMethod(func() any { return &pkgariapi.Agent{} }, func(v any) any {
				return v
			}),
			"list": mockMethod(func() any { return &pkgariapi.ListOptions{} }, func(_ any) any {
				return &pkgariapi.AgentList{Items: []pkgariapi.Agent{
					{Metadata: pkgariapi.ObjectMeta{Name: "ag1"}},
				}}
			}),
			"delete": mockMethod(func() any { return &pkgariapi.ObjectKey{} }, func(_ any) any {
				return json.RawMessage(`{}`)
			}),
		},
	})
}

func mockMethod(newParams func() any, respond func(any) any) jsonrpc.Method {
	return func(_ context.Context, unmarshal func(any) error) (any, error) {
		p := newParams()
		if err := unmarshal(p); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		return respond(p), nil
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Create
// ────────────────────────────────────────────────────────────────────────────

func TestClient_Create_Workspace(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ws := &pkgariapi.Workspace{Metadata: pkgariapi.ObjectMeta{Name: "my-ws"}}
	require.NoError(t, env.client.Create(context.Background(), ws))
	assert.Equal(t, pkgariapi.WorkspacePhasePending, ws.Status.Phase)
}

func TestClient_Create_AgentRun(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ar := &pkgariapi.AgentRun{Metadata: pkgariapi.ObjectMeta{Workspace: "ws1", Name: "ar1"}}
	require.NoError(t, env.client.Create(context.Background(), ar))
	assert.Equal(t, "ws1", ar.Metadata.Workspace)
}

func TestClient_Create_Agent(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ag := &pkgariapi.Agent{Metadata: pkgariapi.ObjectMeta{Name: "claude"}, Spec: pkgariapi.AgentSpec{Command: "bunx"}}
	require.NoError(t, env.client.Create(context.Background(), ag))
	assert.Equal(t, "claude", ag.Metadata.Name)
}

// ────────────────────────────────────────────────────────────────────────────
// Get
// ────────────────────────────────────────────────────────────────────────────

func TestClient_Get_Workspace(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var ws pkgariapi.Workspace
	require.NoError(t, env.client.Get(context.Background(), pkgariapi.ObjectKey{Name: "ws1"}, &ws))
	assert.Equal(t, "ws1", ws.Metadata.Name)
}

func TestClient_Get_AgentRun(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var ar pkgariapi.AgentRun
	require.NoError(t, env.client.Get(context.Background(), pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"}, &ar))
	assert.Equal(t, "ar1", ar.Metadata.Name)
}

func TestClient_Get_Agent(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var ag pkgariapi.Agent
	require.NoError(t, env.client.Get(context.Background(), pkgariapi.ObjectKey{Name: "ag1"}, &ag))
	assert.Equal(t, "ag1", ag.Metadata.Name)
}

// ────────────────────────────────────────────────────────────────────────────
// Update
// ────────────────────────────────────────────────────────────────────────────

func TestClient_Update_Agent(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ag := &pkgariapi.Agent{Metadata: pkgariapi.ObjectMeta{Name: "claude"}, Spec: pkgariapi.AgentSpec{Command: "new-cmd"}}
	require.NoError(t, env.client.Update(context.Background(), ag))
	assert.Equal(t, "new-cmd", ag.Spec.Command)
}

// ────────────────────────────────────────────────────────────────────────────
// List
// ────────────────────────────────────────────────────────────────────────────

func TestClient_List_Workspaces(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var list pkgariapi.WorkspaceList
	require.NoError(t, env.client.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "ws1", list.Items[0].Metadata.Name)
}

func TestClient_List_AgentRuns(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var list pkgariapi.AgentRunList
	require.NoError(t, env.client.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "ar1", list.Items[0].Metadata.Name)
}

func TestClient_List_Agents(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	var list pkgariapi.AgentList
	require.NoError(t, env.client.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "ag1", list.Items[0].Metadata.Name)
}

// ────────────────────────────────────────────────────────────────────────────
// Delete
// ────────────────────────────────────────────────────────────────────────────

func TestClient_Delete_Workspace(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	require.NoError(t, env.client.Delete(context.Background(), pkgariapi.ObjectKey{Name: "ws1"}, &pkgariapi.Workspace{}))
}

func TestClient_Delete_AgentRun(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	require.NoError(t, env.client.Delete(context.Background(), pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"}, &pkgariapi.AgentRun{}))
}

func TestClient_Delete_Agent(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	require.NoError(t, env.client.Delete(context.Background(), pkgariapi.ObjectKey{Name: "ag1"}, &pkgariapi.Agent{}))
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRunOps
// ────────────────────────────────────────────────────────────────────────────

func TestClient_AgentRunOps_Prompt(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	result, err := env.client.AgentRuns().Prompt(context.Background(),
		pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"},
		[]pkgariapi.ContentBlock{pkgariapi.TextBlock("hello")},
	)
	require.NoError(t, err)
	assert.True(t, result.Accepted)
}

func TestClient_AgentRunOps_Cancel(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	require.NoError(t, env.client.AgentRuns().Cancel(context.Background(), pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"}))
}

func TestClient_AgentRunOps_Stop(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	require.NoError(t, env.client.AgentRuns().Stop(context.Background(), pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"}))
}

func TestClient_AgentRunOps_Restart(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ar, err := env.client.AgentRuns().Restart(context.Background(), pkgariapi.ObjectKey{Workspace: "ws1", Name: "ar1"})
	require.NoError(t, err)
	assert.Equal(t, "ws1", ar.Metadata.Workspace)
	assert.Equal(t, "ar1", ar.Metadata.Name)
}

// ────────────────────────────────────────────────────────────────────────────
// WorkspaceOps
// ────────────────────────────────────────────────────────────────────────────

func TestClient_WorkspaceOps_Send(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	result, err := env.client.Workspaces().Send(context.Background(), &pkgariapi.WorkspaceSendParams{
		Workspace: "ws1",
		From:      "a1",
		To:        "a2",
		Message:   []pkgariapi.ContentBlock{pkgariapi.TextBlock("hi")},
	})
	require.NoError(t, err)
	assert.True(t, result.Delivered)
}

// ────────────────────────────────────────────────────────────────────────────
// Lifecycle
// ────────────────────────────────────────────────────────────────────────────

func TestClient_SubInterfaces(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	assert.NotNil(t, env.client.AgentRuns())
	assert.NotNil(t, env.client.Workspaces())
}

func TestClient_DisconnectNotify(t *testing.T) {
	t.Parallel()
	env := newTestClientEnv(t)
	ch := env.client.DisconnectNotify()
	assert.NotNil(t, ch)
}
