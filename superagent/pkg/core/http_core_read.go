package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

const coreResourcePrefix = "/v1/core/"

func isTrustedResourceRead(path string) bool {
	if path == watchdogReadPath || path == telemetryPerformancePath || strings.HasPrefix(path, telemetryTracePath+"/") {
		return true
	}
	rest, ok := strings.CutPrefix(path, coreResourcePrefix)
	if !ok {
		return false
	}
	collection, suffix, _ := strings.Cut(rest, "/")
	switch collection {
	case "trigger-messages", "outbox-messages", "threads", "topics", "issues", "reports", "internal-events":
		return true
	case "session-bindings":
		_, view, _ := strings.Cut(suffix, "/")
		return view == "" || view == "history" || view == "live"
	}
	return false
}

type coreListInput struct {
	Limit     string `query:"limit" hidden:"true"`
	Cursor    string `query:"cursor" doc:"Opaque cursor returned by the preceding page"`
	Order     string `query:"order" doc:"Use newest to read in reverse chronological order"`
	Preview   string `query:"preview" hidden:"true"`
	Trace     string `query:"trace" hidden:"true"`
	ThreadID  string `query:"threadId" hidden:"true"`
	TopicID   string `query:"topicId" hidden:"true"`
	IssueID   string `query:"issueId" hidden:"true"`
	MessageID string `query:"messageId" hidden:"true"`
	View      string `query:"view" hidden:"true"`
	Search    string `query:"search" hidden:"true"`
}

type threadReadInput struct {
	ID string `path:"threadId" doc:"Full Thread ID, encoded once as one path segment"`
}

type topicReadInput struct {
	ID string `path:"topicId"`
}

type issueReadInput struct {
	Trace string `query:"trace" hidden:"true"`
	View  string `query:"view" doc:"Use summary for bounded current Issue facts"`
	ID    string `path:"issueId"`
}

type reportReadInput struct {
	Trace string `query:"trace" hidden:"true"`
	ID    string `path:"reportId"`
}

type internalEventReadInput struct {
	Trace string `query:"trace" hidden:"true"`
	ID    string `path:"eventId"`
}

type bindingReadInput struct {
	Trace string `query:"trace" hidden:"true"`
	ID    string `path:"bindingId"`
}

type topicActivityInput struct {
	ID      string `path:"topicId"`
	View    string `query:"view" doc:"Use conversation to read conversation events"`
	Filter  string `query:"filter" doc:"Activity filters are all, message and issue. Conversation filters are all, router_routed, thread_direct, issue_created, issue_session_dispatch and report."`
	EventID string `query:"eventId" doc:"Find one conversation event. Combine only with view=conversation and the default all filter."`
	Order   string `query:"order" doc:"Activity uses newest order"`
	Limit   string `query:"limit" hidden:"true"`
	Cursor  string `query:"cursor" doc:"Opaque cursor returned by the preceding page"`
}

type topicConversationSummary struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	LastMessageAt string `json:"lastMessageAt,omitempty"`
}

type coreReadResponse struct {
	Status      int
	ContentType string `header:"Content-Type" hidden:"true"`
	Body        []byte
}

type coreHeadResponse struct {
	Status      int
	ContentType string `header:"Content-Type" hidden:"true"`
}

func (s *Server) registerCoreReadResources(api *httpAPI) {
	thread := api.schema(reflect.TypeFor[threadReadProjection]())
	topic := api.schema(reflect.TypeFor[Topic]())
	issue := api.projectionSchema(reflect.TypeFor[issueReadProjection]())
	issuePreview := *api.huma.OpenAPI().Components.Schemas.SchemaFromRef(issue.Ref)
	issuePreview.Required = slices.DeleteFunc(slices.Clone(issuePreview.Required), func(field string) bool { return field == "goal" })
	report := api.projectionSchema(reflect.TypeFor[reportReadProjection]())
	event := api.projectionSchema(reflect.TypeFor[internalEventReadProjection]())
	binding := api.projectionSchema(reflect.TypeFor[bindingReadProjection]())
	topicSummary := api.schema(reflect.TypeFor[topicConversationSummary]())

	s.registerCoreList(api, "threads", "Threads", pageSchema(thread), nil)
	s.registerCoreList(api, "topics", "Topics", &huma.Schema{AnyOf: []*huma.Schema{pageSchema(topic), pageSchema(topicSummary)}}, []*huma.Param{
		queryString("view", "Use conversation for activity-ordered Topic summaries"),
		queryString("search", "Case-insensitive Topic title or ID search for the conversation view"),
	})
	s.registerCoreList(api, "issues", "Issues", pageSchema(&issuePreview), []*huma.Param{
		queryString("topicId", "Filter Issues by Topic"), queryString("messageId", "Filter Issues by a material referencing this Trigger Message"),
	})
	s.registerCoreList(api, "reports", "Reports", pageSchema(report), []*huma.Param{queryString("issueId", "Filter Reports by Issue")})
	s.registerCoreList(api, "internal-events", "InternalEvents", pageSchema(event), []*huma.Param{
		queryString("issueId", "Filter Internal Events referencing this Issue"),
		queryString("messageId", "Filter routing events referencing this Trigger Message, including Topic titles in previews"),
	})
	s.registerCoreList(api, "session-bindings", "SessionBindings", pageSchema(binding), []*huma.Param{queryString("issueId", "Filter Session Bindings owned by an Issue")})

	registerCoreRecord(s, api, "threads/{threadId}", "Thread", thread, func(in *threadReadInput) (string, string) { return in.ID, "" })
	registerCoreRecord(s, api, "topics/{topicId}", "Topic", topic, func(in *topicReadInput) (string, string) { return in.ID, "" })
	registerJSONRead(s, api, huma.Operation{
		Path: coreResourcePrefix + "issues/{issueId}", OperationID: "getIssue", Summary: "Read Issue",
		Tags: []string{"Core"}, Parameters: []*huma.Param{traceQuery()},
		Responses: api.readResponses(&huma.Schema{AnyOf: []*huma.Schema{issue, api.schema(reflect.TypeFor[IssueSummary]())}}, true),
	}, func(ctx context.Context, in *issueReadInput, head bool) (json.RawMessage, error) {
		switch in.View {
		case "":
			return s.coreRecord(ctx, "issues", in.ID, in.Trace == "true", !head)
		case "summary":
			return s.store.issueSummary(ctx, in.ID)
		default:
			return nil, problem("invalid_argument", "unknown Issue view")
		}
	})
	registerCoreRecord(s, api, "reports/{reportId}", "Report", report, func(in *reportReadInput) (string, string) { return in.ID, in.Trace })
	registerCoreRecord(s, api, "internal-events/{eventId}", "InternalEvent", event, func(in *internalEventReadInput) (string, string) { return in.ID, in.Trace })
	registerCoreRecord(s, api, "session-bindings/{bindingId}", "SessionBinding", binding, func(in *bindingReadInput) (string, string) { return in.ID, in.Trace })

	activityResponse := pageSchema(api.projectionSchema(reflect.TypeFor[topicActivityProjection]()))
	registerJSONRead(s, api, huma.Operation{
		Path: coreResourcePrefix + "topics/{topicId}/activity", OperationID: "listTopicActivity",
		Summary: "List Topic activity", Tags: []string{"Core"}, Parameters: []*huma.Param{pageLimit(20)},
		Responses: api.readResponses(activityResponse, true),
	}, func(ctx context.Context, in *topicActivityInput, _ bool) (Page, error) {
		q := url.Values{
			"topicId": {in.ID}, "filter": {in.Filter}, "order": {in.Order}, "eventId": {in.EventID}, "limit": {in.Limit}, "cursor": {in.Cursor},
		}
		switch in.View {
		case "":
			if in.EventID != "" {
				return Page{}, problem("invalid_argument", "eventId requires the Topic conversation view")
			}
			return s.store.topicActivity(ctx, q)
		case "conversation":
			return s.store.topicConversation(ctx, q)
		default:
			return Page{}, problem("invalid_argument", "unknown Topic activity view")
		}
	})
	s.registerSessionBindingOperations(api)
}

func (s *Server) registerCoreList(api *httpAPI, kind, name string, schema *huma.Schema, filters []*huma.Param) {
	limitParam := pageLimit(50)
	if kind == "topics" {
		limitParam.Description += ". Defaults to 50 for Topics and 20 for the conversation view."
		limitParam.Schema.Default = nil
	}
	params := append([]*huma.Param{limitParam}, filters...)
	switch kind {
	case "issues", "reports", "session-bindings", "internal-events":
		params = append(params, queryBoolean("preview", "Return the resource preview projection"), traceQuery())
	}
	registerJSONRead(s, api, huma.Operation{
		Path: coreResourcePrefix + kind, OperationID: "list" + name, Summary: "List " + kind,
		Tags: []string{"Core"}, Parameters: params, Responses: api.readResponses(schema, false),
	}, func(ctx context.Context, in *coreListInput, _ bool) (Page, error) {
		if kind == "topics" && in.View != "" {
			if in.View != "conversation" {
				return Page{}, problem("invalid_argument", "unknown Topic list view")
			}
			return s.store.topicConversation(ctx, url.Values{"search": {in.Search}, "limit": {in.Limit}, "cursor": {in.Cursor}})
		}
		limit, err := coreListLimit(in.Limit, 50)
		if err != nil {
			return Page{}, err
		}
		return s.store.listScoped(ctx, QueryRequest{
			Collection: kind, ThreadID: in.ThreadID, TopicID: in.TopicID, IssueID: in.IssueID, MessageID: in.MessageID,
			Cursor: in.Cursor, Limit: limit, preview: in.Preview == "true", newest: in.Order == "newest", observerTrace: in.Trace == "true",
		}, nil, nil)
	})
}

func registerCoreRecord[I any](s *Server, api *httpAPI, resourcePath, name string, schema *huma.Schema, fields func(*I) (id, trace string)) {
	kind, _, _ := strings.Cut(resourcePath, "/")
	op := huma.Operation{
		Path: coreResourcePrefix + resourcePath, OperationID: "get" + name, Summary: "Read " + name,
		Tags: []string{"Core"}, Responses: api.readResponses(schema, true),
	}
	if kind != "threads" && kind != "topics" {
		op.Parameters = []*huma.Param{traceQuery()}
	}
	registerJSONRead(s, api, op, func(ctx context.Context, in *I, head bool) (json.RawMessage, error) {
		id, trace := fields(in)
		return s.coreRecord(ctx, kind, id, trace == "true", !head)
	})
}

func registerJSONRead[I, T any](s *Server, api *httpAPI, op huma.Operation, read func(context.Context, *I, bool) (T, error)) {
	op.Method = http.MethodGet
	op.SkipValidateParams = true // Existing readers validate input and return Core errors.
	head := headOperation(op)
	huma.Register(api.huma, op, func(ctx context.Context, input *I) (*coreReadResponse, error) {
		value, err := read(ctx, input, false)
		return s.encodeResourceResponse(value, err), nil
	})
	huma.Register(api.huma, head, func(ctx context.Context, input *I) (*coreHeadResponse, error) {
		value, err := read(ctx, input, true)
		response := s.encodeResourceResponse(value, err)
		return &coreHeadResponse{Status: response.Status, ContentType: response.ContentType}, nil
	})
}

func (s *Server) encodeResourceResponse(value any, err error) *coreReadResponse {
	status := http.StatusOK
	if err != nil {
		status, value = coreFailure(err)
	}
	// Preserve the SQL JSON projection and the shared credential scrubber.
	body, err := s.responseJSON(context.Background(), value)
	if err != nil {
		return &coreReadResponse{Status: http.StatusServiceUnavailable, ContentType: "text/plain; charset=utf-8", Body: []byte("response encoding failed\n")}
	}
	return &coreReadResponse{Status: status, ContentType: "application/json", Body: []byte(body + "\n")}
}

func (s *Server) observeResourceHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.store == nil || s.store.telemetry == nil || !s.store.telemetry.enabled {
			next.ServeHTTP(w, r)
			return
		}
		observed, request := s.store.telemetry.startHTTP(w, r)
		defer observed.finishRequest()
		next.ServeHTTP(observed, request)
	})
}

func (api *httpAPI) readResponses(schema *huma.Schema, detail bool) map[string]*huma.Response {
	responses := map[string]*huma.Response{
		"200": jsonResponse("Resource query succeeded", schema),
		"400": api.coreError("Invalid query, filter, path parameter or cursor"),
		"403": api.coreError("Request is outside the trusted Host or Origin boundary"),
		"503": api.coreError("Resource query unavailable"),
	}
	if detail {
		responses["404"] = api.coreError("Resource not found")
	}
	return responses
}

func headOperation(op huma.Operation) huma.Operation {
	op.Method = http.MethodHead
	op.OperationID += "Head"
	op.Parameters = slices.Clone(op.Parameters)
	responses := make(map[string]*huma.Response, len(op.Responses))
	for status, response := range op.Responses {
		responses[status] = &huma.Response{Description: response.Description, Headers: response.Headers}
	}
	op.Responses = responses
	return op
}

func queryString(name, description string) *huma.Param {
	return &huma.Param{Name: name, In: "query", Description: description, Schema: &huma.Schema{Type: huma.TypeString}}
}

func queryBoolean(name, description string) *huma.Param {
	return &huma.Param{Name: name, In: "query", Description: description, Schema: &huma.Schema{Type: huma.TypeBoolean}}
}

func traceQuery() *huma.Param {
	return queryBoolean("trace", "Include recorded Trace associations")
}

func pageLimit(defaultLimit int) *huma.Param {
	minLimit, maxLimit := 1.0, 200.0
	return &huma.Param{Name: "limit", In: "query", Description: "Maximum records per page", Schema: &huma.Schema{
		Type: huma.TypeInteger, Minimum: &minLimit, Maximum: &maxLimit, Default: defaultLimit,
	}}
}

func coreListLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, problem("invalid_argument", "limit must be an integer")
	}
	return limit, nil
}

func pageSchema(item *huma.Schema) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, Required: []string{"items"}, Properties: map[string]*huma.Schema{
		"items": {Type: huma.TypeArray, Items: item}, "nextCursor": {Type: huma.TypeString},
	}}
}
