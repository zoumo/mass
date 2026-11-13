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

func TestSystemPromptServiceRejectsBeforeRuntimeStart(t *testing.T) {
	svc, driver, st := newDefaultsFixture(t)
	for _, testCase := range []struct {
		prompt string
		reason string
	}{
		{"bad\x00prompt", "additional_system_prompt_contains_nul"},
		{string([]byte{0xff}), "additional_system_prompt_invalid_utf8"},
	} {
		_, err := svc.CreateSession(t.Context(), resource.Session{
			Metadata: resource.Metadata{Namespace: "one", Name: "prompt"},
			Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{
				Cwd: "/work", SystemPrompt: testCase.prompt,
			}},
		})
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "invalid_argument", public.Code)
		require.Equal(t, testCase.reason, public.Reason)
	}
	page, err := st.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	driver.mu.Lock()
	defer driver.mu.Unlock()
	require.Zero(t, driver.starts)
	require.Empty(t, driver.opens)
}
