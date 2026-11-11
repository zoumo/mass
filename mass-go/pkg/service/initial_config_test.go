package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInitialSessionConfigurationServiceRejectsBeforeEffects(t *testing.T) {
	svc, driver, st := newDefaultsFixture(t)
	for _, bad := range []string{"bad\x00", string([]byte{0xff})} {
		for _, config := range []*runtime.InitialSessionConfig{{Model: bad}, {Effort: bad}} {
			request := resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "configured"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work", Config: config}}}
			_, err := svc.CreateSession(context.Background(), request)
			require.Error(t, err)
			_, err = svc.LoadSession(context.Background(), resource.SessionLoad{Metadata: request.Metadata, RuntimeRef: "agent", Cwd: "/work", SessionID: "native", Config: config})
			require.Error(t, err)
		}
	}
	page, err := st.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	driver.mu.Lock()
	defer driver.mu.Unlock()
	require.Zero(t, driver.starts)
	require.Empty(t, driver.opens)
	require.Empty(t, driver.resumes)
}
