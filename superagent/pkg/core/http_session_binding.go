package core

import (
	"net/http"
	"net/url"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

const sessionBindingPath = coreResourcePrefix + "session-bindings/{bindingId}"

func (s *Server) registerSessionBindingOperations(api *httpAPI) {
	bindingParam := pathParam("bindingId")
	history := huma.Operation{
		Method: http.MethodGet, Path: sessionBindingPath + "/history", OperationID: "getSessionBindingHistory",
		Summary: "Read verified Session history", Tags: []string{"Core"},
		Parameters: []*huma.Param{bindingParam, queryString("cursor", "Opaque cursor returned by the preceding history page")},
		Responses:  api.readResponses(api.schema(reflect.TypeFor[sessionPreviewHistory]()), true),
	}
	live := huma.Operation{
		Method: http.MethodGet, Path: sessionBindingPath + "/live", OperationID: "streamSessionBinding",
		Summary:     "Stream verified Session events",
		Description: "Returns newline-delimited JSON. Each line is a frame. The first frame confirms the connection.",
		Tags:        []string{"Core"}, Parameters: []*huma.Param{bindingParam},
		Responses: api.readResponses(nil, true),
	}
	live.Responses["200"] = &huma.Response{Description: "Session event stream", Content: map[string]*huma.MediaType{
		"application/x-ndjson": {Schema: &huma.Schema{
			Type: huma.TypeString, Description: "UTF-8 JSON frames separated by newlines",
			Extensions: map[string]any{"x-frame-schema": api.schema(reflect.TypeFor[sessionPreviewFrame]())},
		}},
	}}
	for _, view := range []struct {
		name      string
		operation huma.Operation
	}{{"history", history}, {"live", live}} {
		handler := s.observeResourceHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.previewSessionBinding(w, r, sessionBindingPathValue(r), view.name)
		}))
		api.registerRaw(view.operation, handler.ServeHTTP)
		api.registerRaw(headOperation(view.operation), handler.ServeHTTP)
	}
	s.registerSessionChatOperations(api, bindingParam)

	access := "Requires a Core operator Bearer credential and a direct loopback CLI connection."
	api.registerRaw(huma.Operation{
		Method: http.MethodGet, Path: sessionBindingPath + "/runtime-evidence", OperationID: "getSessionBindingRuntimeEvidence",
		Summary: "Read Session Binding runtime evidence", Description: access, Tags: []string{"Operator"},
		Parameters: []*huma.Param{bindingParam}, Security: bearerSecurity(operatorBearer),
		Responses: map[string]*huma.Response{
			"200": jsonResponse("Current binding and observed runtime evidence", api.schema(reflect.TypeFor[SessionBindingInspection]())),
			"401": api.coreError("Operator credential required"),
			"403": api.coreError("Operator request forbidden"),
			"404": api.coreError("Operator access is disabled or the Session Binding does not exist"),
			"503": api.coreError("Evidence unavailable"),
		},
	}, s.observeResourceHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorizeOperator(w, r) {
			s.bindingEvidence(w, r, sessionBindingPathValue(r))
		}
	})).ServeHTTP)
	api.registerRaw(huma.Operation{
		Method: http.MethodPost, Path: sessionBindingPath + "/recoveries", OperationID: "recoverSessionBinding",
		Summary: "Create or replay a Session Binding recovery", Description: access, Tags: []string{"Operator"},
		Parameters: []*huma.Param{bindingParam}, Security: bearerSecurity(operatorBearer),
		RequestBody: &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[bindingRecoveryInput]()))},
		Responses: map[string]*huma.Response{
			"200": jsonResponse("Completed recovery receipt, including an idempotent replay", api.schema(reflect.TypeFor[Recovery]())),
			"400": api.coreError("Invalid recovery request"),
			"401": api.coreError("Operator credential required"),
			"403": api.coreError("Operator request forbidden"),
			"404": api.coreError("Operator access is disabled or the Session Binding does not exist"),
			"409": api.coreError("Recovery conflicts with existing state or cannot be confirmed"),
			"503": api.coreError("Recovery unavailable"),
		},
	}, s.observeResourceHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.recoverBindingInput(w, r, sessionBindingPathValue(r))
	})).ServeHTTP)
}

func pathParam(name string) *huma.Param {
	return &huma.Param{Name: name, In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString}}
}

func sessionBindingPathValue(r *http.Request) string {
	value := r.PathValue("bindingId")
	// Chi matches RawPath when present and otherwise matches the decoded Path.
	if r.URL.RawPath != "" {
		if decoded, err := url.PathUnescape(value); err == nil {
			return decoded
		}
	}
	return value
}
