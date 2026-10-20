package vertical_test

import (
	"context"
	"testing"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// workflowParams describes existing executable scenario tables; no wire envelope.
type workflowParams struct {
	Name         string
	Session      resource.SessionRef
	Runtime      *resource.Runtime
	Create       *resource.Session
	Spec         *resource.RuntimeSpec
	RuntimeQuery resource.RuntimeQuery
	SessionQuery resource.SessionQuery
	History      store.HistoryQuery
	Input        runtime.Input
	Interaction  string
	Answer       any
}

func workflowHTTP(c *client.Client, ctx context.Context, op string, p workflowParams, out any) error {
	switch op {
	case "runtime.create":
		return c.CreateRuntime(ctx, *p.Runtime, out)
	case "runtime.list":
		return c.ListRuntimes(ctx, p.RuntimeQuery, out)
	case "runtime.get":
		return c.GetRuntime(ctx, p.Name, out)
	case "runtime.update":
		return c.UpdateRuntime(ctx, p.Name, *p.Spec, out)
	case "runtime.start":
		return c.StartRuntime(ctx, p.Name, out)
	case "runtime.stop":
		return c.StopRuntime(ctx, p.Name, out)
	case "runtime.delete":
		return c.DeleteRuntime(ctx, p.Name)
	case "session.create":
		return c.CreateSession(ctx, *p.Create, out)
	case "session.list":
		return c.ListSessions(ctx, p.SessionQuery, out)
	case "session.get":
		return c.GetSession(ctx, p.Session, out)
	case "session.input":
		return c.SendPrompt(ctx, p.Session, p.Input, out)
	case "session.answer":
		return c.Answer(ctx, p.Session, p.Interaction, p.Answer, out)
	case "session.history":
		return c.History(ctx, p.Session, p.History, out)
	case "session.cancel":
		return c.Cancel(ctx, p.Session, out)
	case "session.archive":
		return c.ArchiveSession(ctx, p.Session, out)
	case "session.resume":
		return c.ResumeSession(ctx, p.Session, out)
	}
	return transport.Invalid()
}

// newHTTPClient binds client resource cleanup to the current test.
func newHTTPClient(t *testing.T, serverURL, token string) *client.Client {
	t.Helper()
	c := client.New(serverURL, token)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close HTTP client: %v", err)
		}
	})
	return c
}
