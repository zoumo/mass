package sa

import (
	"context"
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

// Ticket DTOs select durable business and execution evidence explicitly. They do
// not expose capabilities, authorization resources, raw MCP or transition data.
type ObserverTicket struct {
	ID                      string    `json:"id"`
	ProjectID               string    `json:"projectId"`
	Title                   string    `json:"title"`
	BusinessState           string    `json:"businessState"`
	ParentTicketID          string    `json:"parentTicketId,omitempty"`
	RootTicketID            string    `json:"rootTicketId"`
	Depth                   int       `json:"depth"`
	CurrentAssignmentID     string    `json:"currentAssignmentId,omitempty"`
	CurrentAssignmentStatus string    `json:"currentAssignmentStatus"`
	CurrentSessionID        string    `json:"currentSessionId,omitempty"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}
type ObserverTicketAssignment struct {
	ID                     string               `json:"id"`
	Current                bool                 `json:"current"`
	WorkerName             string               `json:"workerName"`
	SessionID              string               `json:"sessionId,omitempty"`
	RuntimeRef             string               `json:"runtimeRef"`
	Requested              InitialSessionConfig `json:"requested"`
	Status                 string               `json:"status"`
	ExecutionID            string               `json:"executionId,omitempty"`
	ExecutionState         string               `json:"executionState,omitempty"`
	AwaitingResumedInput   bool                 `json:"awaitingResumedInput"`
	ReconciliationRequired bool                 `json:"reconciliationRequired"`
	ReconciliationReason   string               `json:"reconciliationReason,omitempty"`
	CreatedAt              time.Time            `json:"createdAt"`
	UpdatedAt              time.Time            `json:"updatedAt"`
	StoppedAt              time.Time            `json:"stoppedAt,omitempty"`
	Observation            RouterObservation    `json:"observation"`
}
type ObserverTicketReport struct {
	ID            string    `json:"id"`
	Body          string    `json:"body"`
	AssignmentID  string    `json:"assignmentId"`
	SessionID     string    `json:"sessionId,omitempty"`
	SourceEventID string    `json:"sourceEventId"`
	SourceItemID  string    `json:"sourceItemId,omitempty"`
	SourceTurnID  string    `json:"sourceTurnId,omitempty"`
	Status        string    `json:"status"`
	Structured    bool      `json:"structured"`
	ParseError    string    `json:"parseError,omitempty"`
	ReportedAt    time.Time `json:"reportedAt"`
}
type ObserverTicketTrigger struct {
	TriggerID  string                    `json:"triggerId"`
	Summary    string                    `json:"summary"`
	Source     string                    `json:"source"`
	ThreadID   string                    `json:"threadId"`
	Deliveries []ObserverTriggerDelivery `json:"deliveries"`
}
type ObserverTicketInternalInput struct {
	InboxID          string                 `json:"inboxId"`
	Kind             string                 `json:"kind"`
	Summary          string                 `json:"summary"`
	ChildTicketID    string                 `json:"childTicketId,omitempty"`
	ChildState       string                 `json:"childState,omitempty"`
	Revision         uint64                 `json:"revision"`
	State            string                 `json:"state"`
	TargetResolution string                 `json:"targetResolution"`
	AssignmentID     string                 `json:"assignmentId,omitempty"`
	SessionID        string                 `json:"sessionId,omitempty"`
	ExecutionID      string                 `json:"executionId,omitempty"`
	AcceptedAt       time.Time              `json:"acceptedAt"`
	Batches          []ObserverTriggerBatch `json:"batches"`
}
type ObserverTicketDetail struct {
	ObserverTicket
	Objective           string                        `json:"objective"`
	Acceptance          string                        `json:"acceptance"`
	CreatedByAssignment string                        `json:"createdByAssignment,omitempty"`
	CreatedBySessionID  string                        `json:"createdBySessionId,omitempty"`
	Parent              *ObserverTicket               `json:"parent,omitempty"`
	ParentUnavailable   bool                          `json:"parentUnavailable"`
	Children            []ObserverTicket              `json:"children"`
	Descendants         []ObserverTicket              `json:"descendants"`
	Assignments         []ObserverTicketAssignment    `json:"assignments"`
	Reports             []ObserverTicketReport        `json:"reports"`
	DirectTriggers      []ObserverTicketTrigger       `json:"directTriggers"`
	InternalInputs      []ObserverTicketInternalInput `json:"internalInputs"`
	ObservedAt          time.Time                     `json:"observedAt"`
	Source              string                        `json:"source"`
}

func observerTicketAssignmentBelongs(a AssignmentState, ticket TicketState) bool {
	return a.ID != "" && a.TicketID == ticket.ID && (a.ProjectID == "" || a.ProjectID == ticket.ProjectID)
}
func observerTicketSummary(p ProjectState, ticket TicketState) ObserverTicket {
	row := ObserverTicket{ID: ticket.ID, ProjectID: ticket.ProjectID, Title: visibleText(ticket.Title), BusinessState: visibleText(ticket.BusinessState), ParentTicketID: ticket.ParentTicketID, RootTicketID: ticket.RootTicketID, Depth: ticket.Depth, CurrentAssignmentID: ticket.AssignmentID, CurrentAssignmentStatus: "unassigned", CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt}
	if ticket.AssignmentID != "" {
		row.CurrentAssignmentStatus = "reference_unavailable"
		if a, ok := p.Assignments[ticket.AssignmentID]; ok && observerTicketAssignmentBelongs(a, ticket) {
			row.CurrentAssignmentStatus = visibleText(a.Status)
			row.CurrentSessionID = a.Session.SessionID
		}
	}
	return row
}

func (o *Observer) ticketDetail(ctx context.Context, snapshot State, id, projectID string) (ObserverTicketDetail, error) {
	if projectID == "" {
		return ObserverTicketDetail{}, operationError("invalid_argument", "observer_project_required", "Ticket detail requires its Project identity")
	}
	if err := o.triggerProject(projectID); err != nil {
		return ObserverTicketDetail{}, err
	}
	p := snapshot.Projects[projectID]
	ticket, ok := p.Tickets[id]
	if !ok || ticket.ProjectID != projectID {
		return ObserverTicketDetail{}, operationError("not_found", "observation_ticket_not_found", "Ticket was not found in this Project")
	}
	result := ObserverTicketDetail{ObserverTicket: observerTicketSummary(p, ticket), Objective: visibleText(ticket.Objective), Acceptance: visibleText(ticket.Acceptance), CreatedByAssignment: ticket.CreatedByAssignment, CreatedBySessionID: ticket.CreatedBySession.SessionID, Children: []ObserverTicket{}, Descendants: []ObserverTicket{}, Assignments: []ObserverTicketAssignment{}, Reports: []ObserverTicketReport{}, DirectTriggers: []ObserverTicketTrigger{}, InternalInputs: []ObserverTicketInternalInput{}, ObservedAt: time.Now().UTC(), Source: "durable_mass_sa"}
	if ticket.ParentTicketID != "" {
		if parent, found := p.Tickets[ticket.ParentTicketID]; found && parent.ProjectID == projectID {
			row := observerTicketSummary(p, parent)
			result.Parent = &row
		} else {
			result.ParentUnavailable = true
		}
	}
	// Follow explicit parent edges inside this Project. Root/namespace coincidences
	// are not ownership evidence; visited identities also bound malformed cycles.
	seen := map[string]bool{id: true}
	frontier := []string{id}
	for len(frontier) > 0 {
		next := []string{}
		for _, parentID := range frontier {
			for _, child := range p.Tickets {
				if child.ProjectID != projectID || child.ParentTicketID != parentID || seen[child.ID] {
					continue
				}
				seen[child.ID] = true
				next = append(next, child.ID)
				row := observerTicketSummary(p, child)
				result.Descendants = append(result.Descendants, row)
				if parentID == id {
					result.Children = append(result.Children, row)
				}
			}
		}
		frontier = next
	}
	for _, rows := range [][]ObserverTicket{result.Children, result.Descendants} {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	}
	for _, a := range p.Assignments {
		if !observerTicketAssignmentBelongs(a, ticket) {
			continue
		}
		result.Assignments = append(result.Assignments, ObserverTicketAssignment{ID: a.ID, Current: a.ID == ticket.AssignmentID, WorkerName: visibleText(a.WorkerName), SessionID: a.Session.SessionID, RuntimeRef: visibleText(a.RuntimeRef), Requested: InitialSessionConfig{Model: visibleText(a.Model), Effort: visibleText(a.Effort)}, Status: visibleText(a.Status), ExecutionID: a.ExecutionID, ExecutionState: visibleText(a.ExecutionState), AwaitingResumedInput: a.AwaitingResumedInput, ReconciliationRequired: a.ReconciliationRequired, ReconciliationReason: visibleText(a.ReconciliationReason), CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, StoppedAt: a.StoppedAt})
	}
	sort.Slice(result.Assignments, func(i, j int) bool {
		a, b := result.Assignments[i], result.Assignments[j]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	// Session identity is queried directly. Lookup never creates, resumes or uses
	// namespace/name to substitute another Session for a historical reference.
	if len(result.Assignments) > 0 {
		client, openErr := OpenMASS(ctx, o.config.MASS)
		if client != nil {
			defer client.Close()
		}
		observed := map[string]RouterObservation{}
		for i := range result.Assignments {
			a := &result.Assignments[i]
			observation, found := observed[a.SessionID]
			if !found {
				observation = o.ticketSessionObservation(ctx, client, openErr, projectID, a.SessionID)
				observed[a.SessionID] = observation
			}
			a.Observation = observation
		}
	}
	for _, report := range ticket.Reports {
		result.Reports = append(result.Reports, ObserverTicketReport{ID: report.ID, Body: visibleText(report.Body), AssignmentID: report.AssignmentID, SessionID: report.Session.SessionID, SourceEventID: report.SourceEventID, SourceItemID: report.SourceItemID, SourceTurnID: report.SourceTurnID, Status: visibleText(report.Status), Structured: report.Structured, ParseError: visibleText(report.ParseError), ReportedAt: report.ReportedAt})
	}
	for _, trigger := range p.Triggers {
		direct := false
		for _, ticketID := range trigger.TicketIDs {
			if ticketID == id {
				direct = true
				break
			}
		}
		if !direct {
			continue
		}
		ref := ObserverTicketTrigger{TriggerID: trigger.TriggerID, Summary: visibleText(trigger.Summary), Source: visibleText(trigger.Source), ThreadID: visibleText(trigger.ThreadID), Deliveries: []ObserverTriggerDelivery{}}
		// Build from the same captured Project snapshot used for all Ticket fields.
		for _, event := range triggerInputs(p, trigger.TriggerID) {
			if event.TicketID == id {
				ref.Deliveries = append(ref.Deliveries, observerTicketDelivery(p, event))
			}
		}
		result.DirectTriggers = append(result.DirectTriggers, ref)
	}
	sort.Slice(result.DirectTriggers, func(i, j int) bool { return result.DirectTriggers[i].TriggerID < result.DirectTriggers[j].TriggerID })
	for _, event := range p.Inbox {
		if event.TicketID != id || event.ChildLifecycle == nil {
			continue
		}
		delivery := observerTicketDelivery(p, event)
		fact := event.ChildLifecycle
		result.InternalInputs = append(result.InternalInputs, ObserverTicketInternalInput{InboxID: event.ID, Kind: "child_lifecycle", Summary: visibleText(fact.Summary), ChildTicketID: fact.TicketID, ChildState: visibleText(fact.State), Revision: fact.Revision, State: visibleText(event.State), TargetResolution: delivery.TargetResolution, AssignmentID: delivery.AssignmentID, SessionID: delivery.SessionID, ExecutionID: delivery.ExecutionID, AcceptedAt: event.AcceptedAt, Batches: delivery.Batches})
	}
	sort.Slice(result.InternalInputs, func(i, j int) bool { return result.InternalInputs[i].InboxID < result.InternalInputs[j].InboxID })
	return result, nil
}

func observerTicketDelivery(p ProjectState, event InboxEventState) ObserverTriggerDelivery {
	d := ObserverTriggerDelivery{InboxID: event.ID, TicketID: event.TicketID, Route: visibleText(event.RoutingPath), State: visibleText(event.State), AcceptedAt: event.AcceptedAt, UpdatedAt: event.UpdatedAt, DeliveredAt: event.DeliveredAt, ErrorReason: visibleText(event.ErrorReason), Batches: []ObserverTriggerBatch{}, TargetResolution: "pending_executor", Attempted: observerDeliveryAttempted(event.State, event.ExecutionID)}
	for _, batch := range p.Deliveries {
		for _, id := range batch.EventIDs {
			if id == event.ID {
				b := observerTriggerBatch(p, batch)
				d.Batches = append(d.Batches, b)
				d.Attempted = d.Attempted || b.Attempted
				break
			}
		}
	}
	sort.Slice(d.Batches, func(i, j int) bool { return d.Batches[i].ID < d.Batches[j].ID })
	if d.Attempted {
		d.TargetResolution = "fixed_evidence"
		d.AssignmentID = event.TargetAssignmentID
		d.SessionID = event.TargetSession.SessionID
		d.ExecutionID = event.ExecutionID
	} else if event.State == "blocked" {
		d.TargetResolution = "blocked_before_attempt"
	} else if event.State == "handled" {
		d.TargetResolution = "handled_without_attempt"
	}
	return d
}

func (o *Observer) ticketSessionObservation(ctx context.Context, client *MASSClient, openErr error, project, id string) RouterObservation {
	at := time.Now().UTC()
	if id == "" {
		return RouterObservation{Status: "unavailable", AttemptedAt: at, Error: "Assignment has no durable Session identity"}
	}
	key := project + "\x00" + id
	o.mu.Lock()
	observation := o.ticketSessions[key]
	o.mu.Unlock()
	observation.AttemptedAt = at
	observation.Stale = observation.Session != nil
	observation.Status = "unavailable"
	observation.Error = "massd Session observation unavailable; retry to refresh"
	err := openErr
	var session Session
	if err == nil {
		session, err = client.GetSession(ctx, SessionRef{SessionID: id})
	}
	if IsErrorCode(err, "not_found") {
		observation.Status = "not_found"
		observation.Error = "Session not found in massd; no Session was created or resumed"
		observation.Session = nil
		observation.Stale = false
	}
	if err == nil {
		if session.Status.SessionID != id {
			observation.Error = "massd returned a different Session identity"
		} else {
			model, effort := sessionConfig(session)
			observation = RouterObservation{Status: "observed", AttemptedAt: at, LastSuccessAt: &at, Session: &ObserverSession{SessionID: id, Namespace: visibleText(session.Metadata.Namespace), Name: visibleText(session.Metadata.Name), RuntimeRef: visibleText(session.Spec.RuntimeRef), State: visibleText(session.Status.State), Config: &InitialSessionConfig{Model: visibleText(model), Effort: visibleText(effort)}}}
		}
	}
	o.mu.Lock()
	if o.ticketSessions == nil {
		o.ticketSessions = map[string]RouterObservation{}
	}
	o.ticketSessions[key] = observation
	o.mu.Unlock()
	return observation
}

// A traversal freezes rows, including filter membership and current references.
// It has the same bounded lifetime as Trigger traversals and explicit expiry.
type observerTicketTraversal struct {
	Rows        []ObserverTicket
	Fingerprint [32]byte
	At          time.Time
}
type observerTicketCursor struct {
	ID          string   `json:"id"`
	After       string   `json:"after"`
	Fingerprint [32]byte `json:"filter"`
}

func (o *Observer) ticketPage(snapshot State, query url.Values) (ObserverPage[ObserverTicket], error) {
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return ObserverPage[ObserverTicket]{}, operationError("invalid_argument", "invalid_observer_limit", "limit must be between 1 and 200")
		}
	}
	if id := query.Get("project"); id != "" {
		if err := o.triggerProject(id); err != nil {
			return ObserverPage[ObserverTicket]{}, err
		}
	}
	filters := url.Values{}
	for key, values := range query {
		filters[key] = append([]string(nil), values...)
	}
	filters.Del("cursor")
	fingerprint := sha256.Sum256([]byte(filters.Encode()))
	now := time.Now().UTC()
	cursor := observerTicketCursor{Fingerprint: fingerprint}
	var traversal observerTicketTraversal
	start := 0
	if raw := query.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Fingerprint != fingerprint {
			return ObserverPage[ObserverTicket]{}, invalidTriggerCursor()
		}
		o.mu.Lock()
		traversal = o.ticketPages[cursor.ID]
		o.mu.Unlock()
		if traversal.At.IsZero() || now.Sub(traversal.At) > 15*time.Minute || traversal.Fingerprint != fingerprint {
			return ObserverPage[ObserverTicket]{}, invalidTriggerCursor()
		}
		found := false
		for i, row := range traversal.Rows {
			if row.ProjectID+"/"+row.ID == cursor.After {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return ObserverPage[ObserverTicket]{}, invalidTriggerCursor()
		}
	} else {
		rows := []ObserverTicket{}
		for _, configured := range o.config.Projects {
			if id := query.Get("project"); id != "" && id != configured.ID {
				continue
			}
			p := snapshot.Projects[configured.ID]
			for _, ticket := range p.Tickets {
				if ticket.ProjectID != configured.ID {
					continue
				}
				if query.Get("state") != "" && ticket.BusinessState != query.Get("state") || query.Get("parent") != "" && ticket.ParentTicketID != query.Get("parent") || query.Get("root") != "" && ticket.RootTicketID != query.Get("root") {
					continue
				}
				if search := strings.ToLower(query.Get("search")); search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{ticket.ID, ticket.Title, ticket.Objective, ticket.Acceptance, ticket.AssignmentID}, " ")), search) {
					continue
				}
				rows = append(rows, observerTicketSummary(p, ticket))
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectID+"/"+rows[i].ID < rows[j].ProjectID+"/"+rows[j].ID })
		traversal = observerTicketTraversal{Rows: rows, Fingerprint: fingerprint, At: now}
	}
	end := min(start+limit, len(traversal.Rows))
	next := ""
	if end < len(traversal.Rows) {
		if cursor.ID == "" {
			var id [18]byte
			if _, err := rand.Read(id[:]); err != nil {
				return ObserverPage[ObserverTicket]{}, operationError("unavailable", "observer_cursor_unavailable", "cannot start traversal")
			}
			cursor.ID = base64.RawURLEncoding.EncodeToString(id[:])
			o.mu.Lock()
			if o.ticketPages == nil {
				o.ticketPages = map[string]observerTicketTraversal{}
			}
			for key, page := range o.ticketPages {
				if now.Sub(page.At) > 15*time.Minute {
					delete(o.ticketPages, key)
				}
			}
			if len(o.ticketPages) >= 64 {
				oldest := ""
				var at time.Time
				for key, page := range o.ticketPages {
					if oldest == "" || page.At.Before(at) {
						oldest = key
						at = page.At
					}
				}
				delete(o.ticketPages, oldest)
			}
			o.ticketPages[cursor.ID] = traversal
			o.mu.Unlock()
		}
		cursor.After = traversal.Rows[end-1].ProjectID + "/" + traversal.Rows[end-1].ID
		data, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return ObserverPage[ObserverTicket]{Items: traversal.Rows[start:end], Next: next, ObservedAt: traversal.At, Source: "durable_mass_sa_traversal"}, nil
}
