package core

import (
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	rpc2 "github.com/sourcegraph/jsonrpc2"
)

const (
	triggerBearer  = "triggerBearer"
	operatorBearer = "operatorBearer"
	roleBearer     = "roleBearer"
	massdBearer    = "massdBearer"
)

type httpAPI struct {
	router *chi.Mux
	huma   huma.API
}

// OpenAPI returns the contract registered by the serving router without
// starting Core or constructing its runtime dependencies.
func OpenAPI() *huma.OpenAPI {
	return new(Server).newHTTPAPI().huma.OpenAPI()
}

func (s *Server) newHTTPAPI() *httpAPI {
	router := chi.NewRouter()
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if !s.testDispatch(w, r) {
			s.fail(w, problem("not_found", "route not found"))
		}
	})
	config := huma.DefaultConfig("Superagent HTTP API", "1.0.0")
	config.OpenAPIPath, config.DocsPath, config.SchemasPath = "", "", ""
	config.CreateHooks = nil
	config.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", func(t reflect.Type, hint string) string {
		if t == reflect.TypeFor[rpc2.Error]() || t == reflect.TypeFor[*rpc2.Error]() {
			return "JSONRPCError"
		}
		return huma.DefaultSchemaNamer(t, hint)
	})
	config.Components.Schemas.RegisterTypeAlias(reflect.TypeFor[json.RawMessage](), reflect.TypeFor[any]())
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		triggerBearer:  {Type: "http", Scheme: "bearer", Description: "Core triggers.token credential"},
		operatorBearer: {Type: "http", Scheme: "bearer", Description: "Core admin.token credential for Operators"},
		roleBearer:     {Type: "http", Scheme: "bearer", Description: "MCP role capability"},
		massdBearer:    {Type: "http", Scheme: "bearer", Description: "Core admin.token credential for embedded MASS"},
	}
	api := &httpAPI{router: router, huma: humachi.New(router.With(s.observeResourceHTTP), config)}
	s.registerMCP(api)
	s.registerMessageResources(api)
	s.registerScheduledTaskResources(api)
	s.registerCoreReadResources(api)
	s.registerWatchdogRead(api)
	s.registerTelemetryResources(api)
	s.registerStatus(api)
	s.registerMassd(api)
	return api
}

// registerRaw pairs an operation with a handler that owns wire decoding,
// credential checks or streaming. Its metadata also appears in offline export.
func (api *httpAPI) registerRaw(operation huma.Operation, handler http.HandlerFunc) {
	api.huma.OpenAPI().AddOperation(&operation)
	api.router.MethodFunc(operation.Method, operation.Path, handler)
}

func (api *httpAPI) schema(t reflect.Type) *huma.Schema {
	return api.huma.OpenAPI().Components.Schemas.Schema(t, true, "")
}

func jsonContent(schema *huma.Schema) map[string]*huma.MediaType {
	return map[string]*huma.MediaType{"application/json": {Schema: schema}}
}

func jsonResponse(description string, schema *huma.Schema) *huma.Response {
	return &huma.Response{Description: description, Content: jsonContent(schema)}
}

func bearerSecurity(scheme string) []map[string][]string {
	return []map[string][]string{{scheme: {}}}
}

type coreHTTPError struct {
	Error *Error `json:"error"`
}

func (api *httpAPI) coreError(description string) *huma.Response {
	return jsonResponse(description, api.schema(reflect.TypeFor[coreHTTPError]()))
}

func (s *Server) registerMCP(api *httpAPI) {
	wireResponse := api.schema(reflect.TypeFor[mcpHTTPResponse]())
	api.registerRaw(huma.Operation{
		Method: http.MethodPost, Path: mcpPath, OperationID: "callRoleMcp", Tags: []string{"MCP"},
		Summary:     "Exchange role MCP JSON-RPC messages",
		Security:    bearerSecurity(roleBearer),
		Parameters:  []*huma.Param{{Name: "MCP-Protocol-Version", In: "header", Schema: &huma.Schema{Type: huma.TypeString}}},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[mcpHTTPRequest]()))},
		Responses: map[string]*huma.Response{
			"200": jsonResponse("JSON-RPC reply", wireResponse),
			"202": {Description: "Notification accepted"},
			"400": jsonResponse("Invalid request or protocol version", &huma.Schema{OneOf: []*huma.Schema{wireResponse, api.schema(reflect.TypeFor[coreHTTPError]())}}),
			"401": api.coreError("Role credential required"),
			"403": api.coreError("Role request forbidden"),
			"409": api.coreError("Role binding is stale"),
			"413": {Description: "Request too large"},
			"415": {Description: "Content-Type must be application/json"},
			"503": api.coreError("Core unavailable"),
		},
	}, s.serveMCP)
}
