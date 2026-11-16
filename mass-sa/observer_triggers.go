package sa

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ObserverTriggerTicket struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	CurrentAssignmentID string `json:"currentAssignmentId,omitempty"`
	CurrentSessionID    string `json:"currentSessionId,omitempty"`
}

type ObserverTrigger struct {
	TriggerID       string                  `json:"triggerId"`
	ProjectID       string                  `json:"projectId"`
	Source          string                  `json:"source"`
	SourceEventID   string                  `json:"sourceEventId"`
	ThreadID        string                  `json:"threadId"`
	Kind            string                  `json:"kind"`
	Summary         string                  `json:"summary"`
	AcceptedAt      time.Time               `json:"acceptedAt"`
	Decision        string                  `json:"decision"`
	Routes          []string                `json:"routes"`
	DeliveryStates  []string                `json:"deliveryStates"`
	ReplyCount      int                     `json:"replyCount"`
	Tickets         []ObserverTriggerTicket `json:"tickets"`
	HistoryRecorded bool                    `json:"historyRecorded"`
	EntryReason     string                  `json:"entryReason"`
}

type ObserverTriggerInput struct {
	InboxID   string `json:"inboxId"`
	Kind      string `json:"kind"`
	TriggerID string `json:"triggerId,omitempty"`
	TicketID  string `json:"ticketId,omitempty"`
}

type ObserverTriggerBatch struct {
	Attempted        bool                   `json:"attempted"`
	TargetResolution string                 `json:"targetResolution"`
	ID               string                 `json:"id"`
	State            string                 `json:"state"`
	ExecutionID      string                 `json:"executionId,omitempty"`
	AssignmentID     string                 `json:"assignmentId,omitempty"`
	SessionID        string                 `json:"sessionId,omitempty"`
	CreatedAt        time.Time              `json:"createdAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
	DeliveredAt      time.Time              `json:"deliveredAt,omitempty"`
	ErrorReason      string                 `json:"errorReason,omitempty"`
	Inputs           []ObserverTriggerInput `json:"inputs"`
}

type ObserverTriggerDelivery struct {
	InboxID          string                 `json:"inboxId"`
	TicketID         string                 `json:"ticketId,omitempty"`
	Route            string                 `json:"route"`
	State            string                 `json:"state"`
	AcceptedAt       time.Time              `json:"acceptedAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
	DeliveredAt      time.Time              `json:"deliveredAt,omitempty"`
	Attempted        bool                   `json:"attempted"`
	TargetResolution string                 `json:"targetResolution"`
	AssignmentID     string                 `json:"assignmentId,omitempty"`
	SessionID        string                 `json:"sessionId,omitempty"`
	ExecutionID      string                 `json:"executionId,omitempty"`
	ErrorReason      string                 `json:"errorReason,omitempty"`
	Batches          []ObserverTriggerBatch `json:"batches"`
}

type ObserverTriggerDetail struct {
	ObserverTrigger
	Content        string                    `json:"content"`
	ReplyRequested bool                      `json:"replyRequested"`
	EchoOfOutputID string                    `json:"echoOfOutputId,omitempty"`
	ErrorReason    string                    `json:"errorReason,omitempty"`
	Admission      *TriggerRoutingEvidence   `json:"admission,omitempty"`
	Decisions      []TriggerRoutingEvidence  `json:"decisions"`
	Deliveries     []ObserverTriggerDelivery `json:"deliveries"`
	ObservedAt     time.Time                 `json:"observedAt"`
	SourceKind     string                    `json:"sourceKind"`
}

func observerTriggerSummary(p ProjectState, trigger TriggerState) ObserverTrigger {
	row := ObserverTrigger{
		TriggerID: trigger.TriggerID, ProjectID: trigger.ProjectID, Source: visibleText(trigger.Source), SourceEventID: visibleText(trigger.SourceEventID),
		ThreadID: visibleText(trigger.ThreadID), Kind: visibleText(trigger.Kind), Summary: visibleText(trigger.Summary), AcceptedAt: trigger.AcceptedAt,
		Decision: visibleText(trigger.Decision), Routes: []string{}, DeliveryStates: []string{}, Tickets: []ObserverTriggerTicket{}, HistoryRecorded: trigger.Admission != nil,
		EntryReason: "history_not_recorded",
	}
	if trigger.Admission != nil {
		row.EntryReason = visibleText(trigger.Admission.Reason)
	}
	for _, id := range trigger.TicketIDs {
		ticket := p.Tickets[id]
		ref := ObserverTriggerTicket{ID: id, Title: visibleText(ticket.Title), CurrentAssignmentID: ticket.AssignmentID}
		if assignment, ok := p.Assignments[ticket.AssignmentID]; ok {
			ref.CurrentSessionID = assignment.Session.SessionID
		}
		row.Tickets = append(row.Tickets, ref)
	}
	for _, event := range triggerInputs(p, trigger.TriggerID) {
		row.Routes = observerTriggerAppendUnique(row.Routes, visibleText(event.RoutingPath))
		row.DeliveryStates = observerTriggerAppendUnique(row.DeliveryStates, visibleText(event.State))
	}
	for _, output := range p.Outputs {
		if output.TriggerID == trigger.TriggerID && observerOutputCategory(output.Kind) == "reply" {
			row.ReplyCount++
		}
	}
	if len(row.Routes) == 0 {
		route := "unknown"
		if trigger.Decision == "ignored" {
			route = "ignored"
		}
		row.Routes = append(row.Routes, route)
	}
	return row
}

func observerTriggerAppendUnique(values []string, value string) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	return append(values, value)
}

func triggerInputs(p ProjectState, id string) []InboxEventState {
	events := []InboxEventState{}
	for _, event := range p.Inbox {
		if event.TriggerID == id && event.ChildLifecycle == nil {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Sequence == events[j].Sequence {
			return events[i].ID < events[j].ID
		}
		return events[i].Sequence < events[j].Sequence
	})
	return events
}

func observerTriggerBatch(p ProjectState, batch DeliveryBatchState) ObserverTriggerBatch {
	result := ObserverTriggerBatch{ID: batch.ID, Attempted: observerDeliveryAttempted(batch.State, batch.ExecutionID), TargetResolution: "pre_dispatch_candidate", State: visibleText(batch.State), ExecutionID: batch.ExecutionID,
		AssignmentID: batch.TargetAssignmentID, SessionID: batch.TargetSession.SessionID, CreatedAt: batch.CreatedAt,
		UpdatedAt: batch.UpdatedAt, DeliveredAt: batch.DeliveredAt, ErrorReason: visibleText(batch.ErrorReason), Inputs: []ObserverTriggerInput{}}
	if result.Attempted {
		result.TargetResolution = "fixed_evidence"
	}
	for _, id := range batch.EventIDs {
		input := ObserverTriggerInput{InboxID: id, Kind: "unavailable"}
		if event, ok := p.Inbox[id]; ok {
			input.TicketID = event.TicketID
			input.Kind = "internal_input"
			if event.ChildLifecycle != nil {
				input.Kind = "child_lifecycle"
			} else if _, ok := p.Triggers[event.TriggerID]; ok && event.TriggerID != "" {
				input.Kind = "external_trigger"
				input.TriggerID = event.TriggerID
			}
		}
		result.Inputs = append(result.Inputs, input)
	}
	return result
}

func (o *Observer) triggerProject(id string) error {
	for _, p := range o.config.Projects {
		if p.ID == id {
			return nil
		}
	}
	return operationError("not_found", "observation_project_not_found", "configured Project was not found")
}

func (o *Observer) triggerDetail(snapshot State, id, projectID string) (ObserverTriggerDetail, error) {
	if projectID == "" {
		return ObserverTriggerDetail{}, operationError("invalid_argument", "observer_project_required", "Trigger detail requires its Project identity")
	}
	if err := o.triggerProject(projectID); err != nil {
		return ObserverTriggerDetail{}, err
	}
	p := snapshot.Projects[projectID]
	trigger, ok := p.Triggers[id]
	if !ok {
		return ObserverTriggerDetail{}, operationError("not_found", "observation_trigger_not_found", "Trigger was not found in this Project")
	}
	result := ObserverTriggerDetail{ObserverTrigger: observerTriggerSummary(p, trigger), Content: visibleTriggerContent(trigger.Content),
		ReplyRequested: trigger.ReplyRequested, EchoOfOutputID: trigger.EchoOfOutputID, ErrorReason: visibleText(trigger.ErrorReason),
		Admission: visibleTriggerEvidence(trigger.Admission), Decisions: []TriggerRoutingEvidence{}, Deliveries: []ObserverTriggerDelivery{}, ObservedAt: time.Now().UTC(), SourceKind: "durable_mass_sa"}
	for _, evidence := range trigger.Decisions {
		result.Decisions = append(result.Decisions, *visibleTriggerEvidence(&evidence))
	}
	for _, event := range triggerInputs(p, id) {
		delivery := ObserverTriggerDelivery{InboxID: event.ID, TicketID: event.TicketID, Route: visibleText(event.RoutingPath), State: visibleText(event.State),
			AcceptedAt: event.AcceptedAt, UpdatedAt: event.UpdatedAt, DeliveredAt: event.DeliveredAt, ErrorReason: visibleText(event.ErrorReason), Batches: []ObserverTriggerBatch{}, TargetResolution: "pending_executor"}
		for _, batch := range p.Deliveries {
			for _, eventID := range batch.EventIDs {
				if eventID == event.ID {
					delivery.Batches = append(delivery.Batches, observerTriggerBatch(p, batch))
					break
				}
			}
		}
		sort.Slice(delivery.Batches, func(i, j int) bool {
			a, b := delivery.Batches[i], delivery.Batches[j]
			if a.CreatedAt.Equal(b.CreatedAt) {
				return a.ID < b.ID
			}
			return a.CreatedAt.Before(b.CreatedAt)
		})
		delivery.Attempted = observerDeliveryAttempted(event.State, event.ExecutionID)
		for _, batch := range delivery.Batches {
			if batch.Attempted {
				delivery.Attempted = true
			}
		}
		if delivery.Attempted {
			delivery.TargetResolution = "fixed_evidence"
			delivery.AssignmentID, delivery.SessionID, delivery.ExecutionID = event.TargetAssignmentID, event.TargetSession.SessionID, event.ExecutionID
		} else if event.State == "blocked" {
			delivery.TargetResolution = "blocked_before_attempt"
		} else if event.State == "handled" {
			delivery.TargetResolution = "handled_without_attempt"
		}
		result.Deliveries = append(result.Deliveries, delivery)
	}
	return result, nil
}

// A blocked batch is a preflight failure, not a prompt attempt. Only execution
// references and known dispatch/receipt states establish an attempt; unknown
// future or legacy states cannot silently become fixed execution evidence.
func observerDeliveryAttempted(state, executionID string) bool {
	if executionID != "" {
		return true
	}
	switch state {
	case "dispatching", "delivered", "accepted", "unknown", "failed":
		return true
	}
	return false
}

// Decode JSON escapes for human reading, then render only as text in Vue.
// UseNumber preserves source number precision; no content executes as markup.
func visibleTriggerContent(raw json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return visibleText(string(raw))
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return visibleText(string(raw))
	}
	return visibleText(buffer.String())
}

func visibleTriggerEvidence(evidence *TriggerRoutingEvidence) *TriggerRoutingEvidence {
	if evidence == nil {
		return nil
	}
	result := *evidence
	result.Reason, result.Action, result.Decision = visibleText(result.Reason), visibleText(result.Action), visibleText(result.Decision)
	result.MatchedTicketIDs = visibleTexts(evidence.MatchedTicketIDs)
	return &result
}

// A bounded, expiring in-memory traversal freezes rows and their filter matches.
// New arrivals and later decisions do not move the boundary of an existing page.
// Restart/expiry explicitly rejects the cursor; refreshing starts a new traversal.
type observerTriggerTraversal struct {
	Rows        []ObserverTrigger
	Fingerprint [32]byte
	At          time.Time
}

type observerTriggerCursor struct {
	ID          string   `json:"id"`
	After       string   `json:"after"`
	Fingerprint [32]byte `json:"filter"`
}

func (o *Observer) triggerPage(snapshot State, query url.Values) (ObserverPage[ObserverTrigger], error) {
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return ObserverPage[ObserverTrigger]{}, operationError("invalid_argument", "invalid_observer_limit", "limit must be between 1 and 200")
		}
	}
	if query.Get("project") != "" {
		if err := o.triggerProject(query.Get("project")); err != nil {
			return ObserverPage[ObserverTrigger]{}, err
		}
	}
	var from, to time.Time
	for _, bound := range []struct {
		key    string
		target *time.Time
	}{{"from", &from}, {"to", &to}} {
		if raw := query.Get(bound.key); raw != "" {
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return ObserverPage[ObserverTrigger]{}, operationError("invalid_argument", "invalid_observer_time", "time bounds must use RFC3339 with timezone")
			}
			*bound.target = parsed
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return ObserverPage[ObserverTrigger]{}, operationError("invalid_argument", "invalid_observer_time", "from must not be later than to")
	}
	filters := url.Values{}
	for key, values := range query {
		filters[key] = append([]string(nil), values...)
	}
	filters.Del("cursor")
	fingerprint := sha256.Sum256([]byte(filters.Encode()))
	now := time.Now().UTC()
	cursor := observerTriggerCursor{Fingerprint: fingerprint}
	var traversal observerTriggerTraversal
	start := 0
	if raw := query.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Fingerprint != fingerprint {
			return ObserverPage[ObserverTrigger]{}, invalidTriggerCursor()
		}
		o.mu.Lock()
		traversal = o.triggerPages[cursor.ID]
		o.mu.Unlock()
		if traversal.At.IsZero() || now.Sub(traversal.At) > 15*time.Minute || traversal.Fingerprint != fingerprint {
			return ObserverPage[ObserverTrigger]{}, invalidTriggerCursor()
		}
		found := false
		for i, row := range traversal.Rows {
			if row.ProjectID+"/"+row.TriggerID == cursor.After {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return ObserverPage[ObserverTrigger]{}, invalidTriggerCursor()
		}
	} else {
		rows := []ObserverTrigger{}
		for _, configured := range o.config.Projects {
			if project := query.Get("project"); project != "" && project != configured.ID {
				continue
			}
			p := snapshot.Projects[configured.ID]
			for _, trigger := range p.Triggers {
				if query.Get("source") != "" && trigger.Source != query.Get("source") || query.Get("thread") != "" && trigger.ThreadID != query.Get("thread") || query.Get("decision") != "" && trigger.Decision != query.Get("decision") {
					continue
				}
				if !from.IsZero() && trigger.AcceptedAt.Before(from) || !to.IsZero() && trigger.AcceptedAt.After(to) {
					continue
				}
				row := observerTriggerSummary(p, trigger)
				if route := query.Get("route"); route != "" && !observerTriggerHasRoute(row.Routes, route) {
					continue
				}
				if search := strings.ToLower(query.Get("search")); search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{trigger.TriggerID, trigger.Source, trigger.SourceEventID, trigger.ThreadID, trigger.Kind, trigger.Summary}, " ")), search) {
					continue
				}
				rows = append(rows, row)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].AcceptedAt.Equal(rows[j].AcceptedAt) {
				return rows[i].ProjectID+"/"+rows[i].TriggerID > rows[j].ProjectID+"/"+rows[j].TriggerID
			}
			return rows[i].AcceptedAt.After(rows[j].AcceptedAt)
		})
		traversal = observerTriggerTraversal{Rows: rows, Fingerprint: fingerprint, At: now}
	}
	end := min(start+limit, len(traversal.Rows))
	next := ""
	if end < len(traversal.Rows) {
		if cursor.ID == "" {
			var id [18]byte
			if _, err := rand.Read(id[:]); err != nil {
				return ObserverPage[ObserverTrigger]{}, operationError("unavailable", "observer_cursor_unavailable", "cannot start traversal")
			}
			cursor.ID = base64.RawURLEncoding.EncodeToString(id[:])
			o.mu.Lock()
			if o.triggerPages == nil {
				o.triggerPages = map[string]observerTriggerTraversal{}
			}
			for key, page := range o.triggerPages {
				if now.Sub(page.At) > 15*time.Minute {
					delete(o.triggerPages, key)
				}
			}
			if len(o.triggerPages) >= 64 {
				oldest := ""
				var at time.Time
				for key, page := range o.triggerPages {
					if oldest == "" || page.At.Before(at) {
						oldest, at = key, page.At
					}
				}
				delete(o.triggerPages, oldest)
			}
			o.triggerPages[cursor.ID] = traversal
			o.mu.Unlock()
		}
		cursor.After = traversal.Rows[end-1].ProjectID + "/" + traversal.Rows[end-1].TriggerID
		data, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return ObserverPage[ObserverTrigger]{Items: traversal.Rows[start:end], Next: next, ObservedAt: traversal.At, Source: "durable_mass_sa_traversal"}, nil
}

func observerTriggerHasRoute(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func invalidTriggerCursor() error {
	return operationError("invalid_argument", "invalid_observer_cursor", "cursor is invalid, expired or belongs to another filter; refresh the first page")
}
