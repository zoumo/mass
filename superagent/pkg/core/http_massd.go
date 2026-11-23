package core

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	rpcwire "github.com/zoumo/mass/massd/pkg/jsonrpc"
	"github.com/zoumo/mass/massd/pkg/transport"
)

func (s *Server) registerMassd(api *httpAPI) {
	const exposure = "Available when embedded massd exposure is enabled. A deployment can also mount standalone massd at this prefix."
	api.router.HandleFunc(MassdMountPath+"/*", s.serveMassd)
	errorSchema := api.schema(reflect.TypeFor[transport.Error]())
	for _, probe := range []struct{ path, id, summary string }{
		{"/livez", "massdLiveness", "Check that the massd process can respond"},
		{"/readyz", "massdReadiness", "Check that massd can admit requests"},
	} {
		responses := map[string]*huma.Response{
			"200": jsonResponse("Successful probe", api.schema(reflect.TypeFor[probeState]())),
			"403": api.coreError("Core request host not allowed"),
			"404": api.coreError("massd exposure disabled"),
		}
		if probe.path == "/readyz" {
			responses["503"] = jsonResponse("massd admission closed", errorSchema)
		}
		api.registerRaw(huma.Operation{
			Method: http.MethodGet, Path: MassdMountPath + probe.path, OperationID: probe.id,
			Tags: []string{"massd"}, Summary: probe.summary, Description: exposure, Responses: responses,
		}, s.serveMassd)
	}
	api.registerRaw(huma.Operation{
		Method: http.MethodGet, Path: MassdMountPath + rpcwire.WebSocketPath, OperationID: "connectMassd",
		Tags: []string{"massd"}, Summary: "Upgrade to massd JSON-RPC over WebSocket", Description: exposure,
		Security: bearerSecurity(massdBearer),
		Responses: map[string]*huma.Response{
			"101": {Description: "WebSocket handshake accepted"},
			"400": {Description: "Invalid WebSocket handshake"},
			"401": jsonResponse("massd instance credential required", errorSchema),
			"403": jsonResponse("Core request host or WebSocket Origin not allowed", &huma.Schema{
				OneOf: []*huma.Schema{api.schema(reflect.TypeFor[coreHTTPError]()), errorSchema},
			}),
			"404": api.coreError("massd exposure disabled"),
			"503": jsonResponse("massd admission closed", errorSchema),
		},
	}, s.serveMassd)
}

func (s *Server) serveMassd(w http.ResponseWriter, r *http.Request) {
	if s.massdHTTP == nil {
		s.fail(w, problem("not_found", "route not found"))
		return
	}
	// Keep the original writer for the WebSocket upgrade.
	s.massdHTTP.ServeHTTP(w, r)
}
