package client_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInitialSessionConfigurationClientRejectsInvalidBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid config sent") }))
	defer server.Close()
	c := newHTTPClient(t, server.URL, "")
	for _, bad := range []string{"nul\x00", string([]byte{0xff})} {
		for _, config := range []*runtime.InitialSessionConfig{{Model: bad}, {Effort: bad}} {
			spec := resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work", Config: config}}
			require.Error(t, c.CreateSession(t.Context(), resource.Session{Spec: spec}, nil))
			require.Error(t, c.CreateSessionWithInput(t.Context(), resource.SessionCreateInput{Spec: spec}, nil))
			require.Error(t, c.LoadSession(t.Context(), resource.SessionLoad{SessionID: "native", Config: config}, nil))
		}
	}
}
