package transport_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTicket05ArchiveRouteAndFilterAreAbsent(t *testing.T) {
	handler := server(t, &recordingAPI{})

	archive := request(handler, http.MethodPost, "/apis/v1/sessions/demo/archive", "")
	require.Equal(t, http.StatusNotFound, archive.Code, archive.Body.String())

	filter := request(handler, http.MethodGet, "/apis/v1/sessions?state=archived", "")
	require.Equal(t, http.StatusBadRequest, filter.Code, filter.Body.String())
}
