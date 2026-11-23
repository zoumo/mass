package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestMCPHTTPRouteContract(t *testing.T) {
	api := new(Server).newHTTPAPI()
	operation := api.huma.OpenAPI().Paths[mcpPath].Post
	require.NotNil(t, operation)
	require.Equal(t, bearerSecurity(roleBearer), operation.Security)
	require.Contains(t, operation.Responses, "202")
	require.True(t, api.router.Match(chi.NewRouteContext(), http.MethodPost, mcpPath))

	response := httptest.NewRecorder()
	api.router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, mcpPath, http.NoBody))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
	require.Equal(t, http.MethodPost, response.Header().Get("Allow"))
}
