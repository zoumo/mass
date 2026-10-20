package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

// Contract fault fixture, not native Agent evidence: a Driver returns an illegal
// restore identity and fails its first release. Observe settlement via Service.
type invalidRestoreDriver struct {
	runtime.Driver
	pipe          *events.EventPipe
	id            string
	releaseFailed bool
}

func (d *invalidRestoreDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *invalidRestoreDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (d *invalidRestoreDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{SessionID: d.id}, nil
}
func (d *invalidRestoreDriver) CloseSession(_ context.Context, id string) error {
	if id != d.id {
		return errors.New("wrong cleanup identity")
	}
	if !d.releaseFailed {
		d.releaseFailed = true
		return errors.New("injected unknown release")
	}
	return nil
}
func (d *invalidRestoreDriver) Stop(context.Context) error { d.pipe.End(nil); return nil }

type invalidRestoreFactory struct{ driver *invalidRestoreDriver }

func (f invalidRestoreFactory) Validate(runtime.Config) error { return nil }
func (f invalidRestoreFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func TestInvalidRestoreIdentityRetainsRetryableCleanup(t *testing.T) {
	for _, id := range []string{".", ".."} {
		t.Run(id, func(t *testing.T) {
			c, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			st, err := file.Open(t.TempDir(), false)
			require.NoError(t, err)
			defer st.Close()
			original, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}, Status: resource.SessionStatus{SessionID: "original", State: "offline"}})
			require.NoError(t, err)
			driver := &invalidRestoreDriver{pipe: events.NewEventPipe(1), id: id}
			svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fault": invalidRestoreFactory{driver}}))
			defer func() { driver.pipe.End(nil) }()
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault", Config: runtime.Config{}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(c, "agent")
			require.NoError(t, err)
			_, err = svc.ResumeSession(c, resource.SessionRef{SessionID: "original"})
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, "outcome_unknown", public.Code)
			require.NoError(t, svc.ReconcileOpening(c, "agent", runtime.Address{Namespace: "one", SessionName: "demo"}))
			unchanged, err := svc.GetSession(resource.SessionRef{SessionID: "original"})
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
			_, err = svc.StopRuntime(c, "agent")
			require.NoError(t, err)
		})
	}
}
