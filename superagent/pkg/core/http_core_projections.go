package core

import (
	"encoding/json"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// The store assembles these projections from saved resources and related data.
// They describe the HTTP responses without decoding and re-encoding stored JSON.
type threadReadProjection struct {
	Key           string         `json:"key"`
	CreatedAt     string         `json:"createdAt"`
	LastMessageAt *string        `json:"lastMessageAt" nullable:"true"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type traceReadProjection struct {
	TraceAssociations []traceAssociationProjection `json:"traceAssociations,omitempty"`
}

type traceAssociationProjection struct {
	Phase   string `json:"phase"`
	TraceID string `json:"traceId"`
}

type issueReadProjection struct {
	Issue
	traceReadProjection
	LatestRequirementsNotification *internalEventReadProjection            `json:"latestRequirementsNotification,omitempty"`
	LifecycleTraces                map[string][]traceAssociationProjection `json:"lifecycleTraces,omitempty"`
	SessionObservation             *issueSessionObservation                `json:"issueSession,omitempty"`
	TopicTitle                     *string                                 `json:"topicTitle,omitempty" nullable:"true"`
}

type reportReadProjection struct {
	Report
	traceReadProjection
	Current bool `json:"current"`
}

type timerReadProjection struct {
	Timer
	traceReadProjection
	Followup *timerFollowupProjection `json:"followup,omitempty"`
}

type timerFollowupProjection struct {
	ID        string  `json:"event_id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	SentAt    *string `json:"sentAt" nullable:"true"`
	MessageID string  `json:"messageId"`
	TopicID   *string `json:"topicId" nullable:"true"`
}

type bindingReadProjection struct {
	SessionBinding
	traceReadProjection
	Startup             *internalEventReadProjection `json:"startup,omitempty"`
	Reminder            *internalEventReadProjection `json:"reminder,omitempty"`
	ReminderScheduled   bool                         `json:"reminderScheduled,omitempty"`
	ReminderEligibility string                       `json:"reminderEligibility,omitempty"`
}

type internalEventReadProjection struct {
	traceReadProjection
	ID              string           `json:"event_id"`
	Kind            string           `json:"kind"`
	CreatedAt       string           `json:"createdAt"`
	Status          string           `json:"status"`
	SentAt          *string          `json:"sentAt" nullable:"true"`
	Sender          eventIdentity    `json:"sender"`
	Recipient       eventIdentity    `json:"recipient"`
	Refs            []eventReference `json:"refs"`
	Content         json.RawMessage  `json:"content,omitempty"`
	Body            json.RawMessage  `json:"body,omitempty"`
	Revision        *int             `json:"revision,omitempty" nullable:"true"`
	MessageID       *string          `json:"messageId,omitempty" nullable:"true"`
	TopicID         *string          `json:"topicId,omitempty" nullable:"true"`
	TopicTitle      *string          `json:"topicTitle,omitempty" nullable:"true"`
	ReturnedEventID *string          `json:"returnedEventId,omitempty" nullable:"true"`
	Returned        bool             `json:"returned,omitempty"`
	GoalUpdate      bool             `json:"goalUpdate,omitempty"`
}

type topicActivityProjection struct {
	traceReadProjection
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	At               string          `json:"at"`
	Actor            *string         `json:"actor,omitempty" nullable:"true"`
	Recipient        string          `json:"recipient,omitempty"`
	TopicID          string          `json:"topicId,omitempty"`
	IssueID          *string         `json:"issueId,omitempty" nullable:"true"`
	MessageID        *string         `json:"messageId,omitempty" nullable:"true"`
	ThreadKey        string          `json:"threadKey,omitempty"`
	Title            *string         `json:"title,omitempty" nullable:"true"`
	Status           string          `json:"status,omitempty"`
	EventID          *string         `json:"eventId,omitempty" nullable:"true"`
	ReturnedEventID  *string         `json:"returnedEventId,omitempty" nullable:"true"`
	SessionBindingID string          `json:"sessionBindingId,omitempty"`
	ReportID         string          `json:"reportId,omitempty"`
	ReportKind       string          `json:"reportKind,omitempty"`
	Historical       bool            `json:"historical,omitempty"`
	Summary          string          `json:"summary,omitempty"`
	DispatchKind     string          `json:"dispatchKind,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
}

func (api *httpAPI) projectionSchema(t reflect.Type) *huma.Schema {
	ref := api.schema(t)
	schema := api.huma.OpenAPI().Components.Schemas.SchemaFromRef(ref.Ref)
	schema.AdditionalProperties = true
	if t == reflect.TypeFor[issueReadProjection]() {
		reports := schema.Properties["reports"].Items
		api.huma.OpenAPI().Components.Schemas.SchemaFromRef(reports.Ref).AdditionalProperties = true
	}
	if t == reflect.TypeFor[bindingReadProjection]() {
		for _, field := range []string{"startup", "reminder"} {
			schema.Properties[field] = &huma.Schema{AnyOf: []*huma.Schema{schema.Properties[field], {Type: "null"}}}
		}
	}
	return ref
}
