package core

import (
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/internal/triggeridentity"
)

// The SDK flattens the selected branch when encoding a ContentBlock.
// Describe that wire shape for both Trigger Message input and reads.
type triggerContentSchema struct{}

func (triggerContentSchema) Schema(registry huma.Registry) *huma.Schema {
	text := &huma.Schema{Type: huma.TypeString}
	annotations := &huma.Schema{Type: huma.TypeObject, AdditionalProperties: false, Properties: map[string]*huma.Schema{
		"audience": {Type: huma.TypeArray, Items: text},
		"priority": {Type: huma.TypeNumber}, "lastModified": {Type: huma.TypeString, Format: "date-time"},
	}}
	resource := &huma.Schema{Type: huma.TypeObject, AdditionalProperties: false, Required: []string{"uri"}, Properties: map[string]*huma.Schema{
		"uri": text, "mimeType": text, "text": text, "blob": text,
	}}
	return &huma.Schema{OneOf: []*huma.Schema{
		triggerContentVariant("text", annotations, []string{"text"}, map[string]*huma.Schema{"text": text}),
		triggerContentVariant("image", annotations, []string{"data", "mimeType"}, map[string]*huma.Schema{"data": text, "mimeType": text, "uri": text}),
		triggerContentVariant("audio", annotations, []string{"data", "mimeType"}, map[string]*huma.Schema{"data": text, "mimeType": text}),
		triggerContentVariant("resource", annotations, []string{"resource"}, map[string]*huma.Schema{"resource": resource}),
		triggerContentVariant("resource_link", annotations, []string{"uri", "name"}, map[string]*huma.Schema{
			"uri": text, "name": text, "mimeType": text, "title": text, "description": text,
			"size": {Type: huma.TypeInteger}, "icons": {Type: huma.TypeArray, Items: registry.Schema(reflect.TypeFor[events.ResourceIcon](), true, "")},
		}),
	}}
}

func triggerContentVariant(kind string, annotations *huma.Schema, required []string, properties map[string]*huma.Schema) *huma.Schema {
	properties["type"] = &huma.Schema{Type: huma.TypeString, Const: kind}
	properties["annotations"] = annotations
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: false, Properties: properties, Required: append([]string{"type"}, required...)}
}

type messageTrace struct {
	Phase   string `json:"phase"`
	TraceID string `json:"traceId"`
}

type triggerMessageRecord struct {
	ScheduledTaskID   string                 `json:"scheduledTaskId,omitempty"`
	SourceMessageID   string                 `json:"sourceMessageId"`
	ThreadID          string                 `json:"threadId"`
	Content           []events.ContentBlock  `json:"content,omitempty"`
	Meta              json.RawMessage        `json:"_meta,omitempty"`
	ID                string                 `json:"id"`
	Source            string                 `json:"source"`
	AccountID         string                 `json:"accountId"`
	ReceivedAt        string                 `json:"receivedAt"`
	Acceptance        string                 `json:"acceptance"`
	EventID           string                 `json:"eventId,omitempty"`
	Text              string                 `json:"text,omitempty"`
	Routing           *routingStatus         `json:"routing,omitempty" nullable:"true"`
	Relations         []Relation             `json:"relations,omitempty"`
	QuotedReply       *quotedReplyRecordHTTP `json:"quotedReply,omitempty"`
	TraceAssociations []messageTrace         `json:"traceAssociations,omitempty"`
}

type quotedReplyRecordHTTP struct {
	OutboxID   string      `json:"outboxId"`
	BlockerRef *BlockerRef `json:"blockerRef,omitempty"`
}

type routingStatus struct{}

func (routingStatus) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type: huma.TypeObject, Nullable: true, AdditionalProperties: false,
		Required: []string{"round", "state"},
		Properties: map[string]*huma.Schema{
			"round": {Type: huma.TypeInteger},
			"state": {Type: huma.TypeString},
		},
	}
}

type routerMessageRecord struct {
	ID                 string                `json:"id"`
	Text               string                `json:"text"`
	Source             string                `json:"source"`
	State              *string               `json:"state"`
	ActivityAt         string                `json:"activityAt"`
	ActivityID         string                `json:"activityId"`
	ActivityKind       string                `json:"activityKind"`
	ActivityReason     *string               `json:"activityReason"`
	ReturnedEventID    *string               `json:"returnedEventId"`
	ActivityTopicID    *string               `json:"activityTopicId"`
	ActivityTopicTitle *string               `json:"activityTopicTitle"`
	Targets            []routerMessageTarget `json:"targets"`
}

type routerMessageTarget struct {
	ID      string  `json:"id"`
	TopicID string  `json:"topicId"`
	Title   *string `json:"title"`
	Reason  *string `json:"reason"`
}

type outboxHistoryRecord struct {
	ID string `json:"id"`
	OutboxState
	MessageID         string             `json:"messageId"`
	Content           *StructuredContent `json:"content,omitempty"`
	BlockerRef        *BlockerRef        `json:"blockerRef,omitempty"`
	Preview           *string            `json:"preview,omitempty"`
	TraceAssociations []messageTrace     `json:"traceAssociations,omitempty"`
}

type outboxDeliveryRecord struct {
	OutboxItem
	Source      string          `json:"source"`
	AccountID   string          `json:"accountId"`
	ReplyTarget json.RawMessage `json:"replyTarget,omitempty"`
}

type messagePage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (api *httpAPI) registerMessageRead(operation huma.Operation, handler http.HandlerFunc) {
	api.registerRaw(operation, handler)
	operation.Method = http.MethodHead
	operation.OperationID += "Head"
	responses := make(map[string]*huma.Response, len(operation.Responses))
	for status, response := range operation.Responses {
		responses[status] = &huma.Response{Description: response.Description}
	}
	operation.Responses = responses
	api.registerRaw(operation, handler)
}

func (api *httpAPI) messageOperation(method, path, id, description string, response *huma.Schema) huma.Operation {
	return huma.Operation{
		Method: method, Path: path, OperationID: id, Summary: description, Tags: []string{"Core"},
		Responses: map[string]*huma.Response{
			"200": jsonResponse("Success", response),
			"400": api.coreError("Invalid request"),
			"401": api.coreError("Shared Trigger credential required"),
			"403": api.coreError("Request outside its authorized scope"),
			"404": api.coreError("Resource not found"),
			"503": api.coreError("Core unavailable"),
		},
	}
}

func (s *Server) registerMessageResources(api *httpAPI) {
	registry := api.huma.OpenAPI().Components.Schemas
	registry.RegisterTypeAlias(reflect.TypeFor[events.ContentBlock](), reflect.TypeFor[triggerContentSchema]())

	receipt := api.schema(reflect.TypeFor[Receipt]())
	triggerPost := api.messageOperation(http.MethodPost, triggerMessagesPath, "submitTriggerMessage", "Submit a Trigger Message and return the durable receipt", receipt)
	triggerPost.RequestBody = &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[SubmitTriggerMessageRequest]()))}
	triggerPost.Parameters = triggerIdentityHeaders(true, false)
	triggerPost.Security = bearerSecurity(triggerBearer)
	triggerPost.Responses["409"] = api.coreError("Message identity or quoted reply conflicts with persisted state")
	api.registerRaw(triggerPost, s.observeMessageHTTP(s.postTriggerMessage))

	triggerList := api.messageOperation(http.MethodGet, triggerMessagesPath, "listTriggerMessages", "List Trigger Messages, query the Router view or read a Source receipt", &huma.Schema{AnyOf: []*huma.Schema{
		api.schema(reflect.TypeFor[messagePage[triggerMessageRecord]]()),
		api.schema(reflect.TypeFor[messagePage[routerMessageRecord]]()), receipt,
	}})
	triggerList.Parameters = append(messageListParams(),
		messageQueryParam("view", "Router projection sorted by latest Router activity", &huma.Schema{Type: huma.TypeString, Enum: []any{"router"}}),
		messageQueryParam("sourceMessageId", "Return the receipt in the declared Source and Account. Supply this query parameter alone", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("threadId", "Exact Thread ID in the default view", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("messageId", "Exact Trigger Message ID in the Router view", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("source", "Exact Source in the default view. Requires Operator access", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("scheduledTaskId", "Exact scheduled task ID in the default view. Requires Operator access", &huma.Schema{Type: huma.TypeString}),
	)
	triggerList.Description = "The default view and Router view use trusted reads. The source and scheduledTaskId filters require Operator access through a direct Core connection. A sourceMessageId lookup requires the shared Trigger bearer and both Source and Account headers. It returns the acceptance receipt."
	triggerList.Parameters = append(triggerList.Parameters, triggerIdentityHeaders(false, false)...)
	triggerList.Security = append(bearerSecurity(triggerBearer), bearerSecurity(operatorBearer)[0], map[string][]string{})
	triggerList.Extensions = map[string]any{
		"x-conditional-security": map[string]any{
			"sourceMessageId": bearerSecurity(triggerBearer), "source": bearerSecurity(operatorBearer), "scheduledTaskId": bearerSecurity(operatorBearer),
		},
		"x-conditional-headers": map[string][]string{"sourceMessageId": {triggeridentity.SourceHeader, triggeridentity.AccountHeader}},
	}
	api.registerMessageRead(triggerList, s.observeMessageHTTP(s.readTriggerMessages))

	triggerRead := api.messageOperation(http.MethodGet, triggerMessagesPath+"/{messageId}", "getTriggerMessage", "Read Trigger Message details", api.schema(reflect.TypeFor[triggerMessageRecord]()))
	triggerRead.Parameters = []*huma.Param{messagePathParam("messageId"), messageTraceParam()}
	delete(triggerRead.Responses, "401")
	api.registerMessageRead(triggerRead, s.observeMessageHTTP(s.readTriggerMessage))

	s.registerOutboxMessages(api)
}

func (s *Server) registerOutboxMessages(api *httpAPI) {
	delivery := messageQueryParam("view", "Select Account-scoped delivery fields for the configured consumer", &huma.Schema{Type: huma.TypeString, Enum: []any{"delivery"}})
	list := api.messageOperation(http.MethodGet, outboxMessagesPath, "listOutboxMessages", "List Outbox Message history or eligible deliveries", &huma.Schema{AnyOf: []*huma.Schema{
		api.schema(reflect.TypeFor[messagePage[outboxHistoryRecord]]()),
		api.schema(reflect.TypeFor[messagePage[outboxDeliveryRecord]]()),
	}})
	list.Parameters = append(messageListParams(), delivery,
		messageQueryParam("messageId", "Exact Trigger Message ID in the history view", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("topicId", "Topic routing history filter in the history view", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("threadId", "Exact Thread ID in the history view", &huma.Schema{Type: huma.TypeString}),
	)
	list.Description = "The history view uses the trusted-read boundary. The delivery view requires the shared Trigger bearer and the Source, Account and Instance headers of the configured consumer. It returns pending or retry-eligible records using limit and cursor."
	list.Parameters = append(list.Parameters, triggerIdentityHeaders(false, true)...)
	list.Security = append(bearerSecurity(triggerBearer), map[string][]string{})
	list.Extensions = map[string]any{
		"x-conditional-security": map[string]any{"view=delivery": bearerSecurity(triggerBearer)},
		"x-conditional-headers":  map[string][]string{"view=delivery": {triggeridentity.SourceHeader, triggeridentity.AccountHeader, triggeridentity.InstanceHeader}},
	}
	api.registerMessageRead(list, s.observeMessageHTTP(s.readOutboxMessages))

	path := outboxMessagesPath + "/{outboxId}"
	read := api.messageOperation(http.MethodGet, path, "getOutboxMessage", "Read an Outbox Message in history or delivery view", &huma.Schema{AnyOf: []*huma.Schema{
		api.schema(reflect.TypeFor[outboxHistoryRecord]()), api.schema(reflect.TypeFor[outboxDeliveryRecord]()),
	}})
	read.Description = "History uses the trusted-read boundary. Delivery requires the shared Trigger bearer and the Source, Account and Instance headers of the configured consumer. The delivery view includes the reply target when present. Terminal states are readable."
	read.Parameters = []*huma.Param{messagePathParam("outboxId"), delivery, messageTraceParam()}
	read.Parameters = append(read.Parameters, triggerIdentityHeaders(false, true)...)
	read.Security = list.Security
	read.Extensions = list.Extensions
	api.registerMessageRead(read, s.observeMessageHTTP(s.readOutboxMessages))

	patch := api.messageOperation(http.MethodPatch, path, "patchOutboxMessage", "Record an Outbox delivery outcome using the expected state", api.schema(reflect.TypeFor[outboxWriteReceipt]()))
	patch.Parameters = append([]*huma.Param{messagePathParam("outboxId")}, triggerIdentityHeaders(true, true)...)
	patch.Security = bearerSecurity(triggerBearer)
	patch.RequestBody = &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[outboxPatch]()))}
	patch.Responses["409"] = api.coreError("Expected state conflicts or record is ineligible for the requested update")
	api.registerRaw(patch, s.observeMessageHTTP(s.patchOutboxMessage))
}

func triggerIdentityHeaders(required, withInstance bool) []*huma.Param {
	headers := []*huma.Param{
		{Name: triggeridentity.SourceHeader, In: "header", Required: required, Description: "External Source declared by the Trigger", Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: triggeridentity.AccountHeader, In: "header", Required: required, Description: "Account ID declared by the Trigger", Schema: &huma.Schema{Type: huma.TypeString}},
	}
	if withInstance {
		headers = append(headers, &huma.Param{
			Name: triggeridentity.InstanceHeader, In: "header", Required: required, Description: "Configured Outbox consumer instance ID",
			Schema: &huma.Schema{Type: huma.TypeString, Pattern: triggeridentity.InstancePattern},
		})
	}
	return headers
}

func messagePathParam(name string) *huma.Param {
	return &huma.Param{Name: name, In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString}}
}

func messageQueryParam(name, description string, schema *huma.Schema) *huma.Param {
	return &huma.Param{Name: name, In: "query", Description: description, Schema: schema}
}

func messageTraceParam() *huma.Param {
	return messageQueryParam("trace", "Include Trace associations where available", &huma.Schema{Type: huma.TypeBoolean})
}

func messageListParams() []*huma.Param {
	minLimit, maxLimit := float64(1), float64(200)
	return []*huma.Param{
		messageQueryParam("limit", "Page size. Defaults to 50, or 20 for the Router view", &huma.Schema{Type: huma.TypeInteger, Minimum: &minLimit, Maximum: &maxLimit}),
		messageQueryParam("cursor", "Opaque continuation for the same view and filters", &huma.Schema{Type: huma.TypeString}),
		messageQueryParam("order", "Use newest-first ordering in the default view", &huma.Schema{Type: huma.TypeString, Enum: []any{"newest"}}),
		messageQueryParam("preview", "Read compact rows where supported", &huma.Schema{Type: huma.TypeBoolean}),
		messageTraceParam(),
	}
}
