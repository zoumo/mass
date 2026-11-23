package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreProbesWithoutAdmission(t *testing.T) {
	router := new(Server).newHTTPAPI().router
	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", http.NoBody))
	require.Equal(t, http.StatusOK, live.Code)
	require.JSONEq(t, `{"status":"alive"}`, live.Body.String())

	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	require.JSONEq(t, `{"status":"unavailable"}`, ready.Body.String())
}
