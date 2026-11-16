package sa

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These DTOs deliberately do not embed storage records. Observation is not a
// consumer read and must never call ReadOutputs or update its checkpoint.
type ObserverOutputAudit struct {
	Consumer    string     `json:"consumer"`
	Source      string     `json:"source"`
	ThreadID    string     `json:"threadId"`
	ReadAt      time.Time  `json:"readAt"`
	Outcome     string     `json:"outcome,omitempty"`
	Reference   string     `json:"reference,omitempty"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

type ObserverOutput struct {
	ID         string                `json:"id"`
	Sequence   uint64                `json:"sequence"`
	ProjectID  string                `json:"projectId"`
	Category   string                `json:"category"`
	Kind       string                `json:"kind"`
	TriggerID  string                `json:"triggerId,omitempty"`
	TicketID   string                `json:"ticketId,omitempty"`
	FactID     string                `json:"factId,omitempty"`
	Summary    string                `json:"summary"`
	Evidence   []string              `json:"evidence"`
	Targets    []OutputTarget        `json:"targets"`
	RecordedAt time.Time             `json:"recordedAt"`
	Audit      []ObserverOutputAudit `json:"audit"`
}

func observerOutputCategory(kind string) string {
	switch kind {
	case "reply.accepted", "reply.clarification", "reply.no_execution", "reply.summary":
		return "reply"
	}
	if strings.HasPrefix(kind, "ticket.") {
		return "fact"
	}
	return "other"
}

func observerOutput(p ProjectState, record OutputRecord) ObserverOutput {
	row := ObserverOutput{
		ID: record.ID, ProjectID: record.ProjectID, Sequence: record.Sequence,
		Category: observerOutputCategory(record.Kind), Kind: visibleText(record.Kind),
		TriggerID: record.TriggerID, TicketID: record.TicketID, FactID: record.FactID,
		Summary: visibleText(record.Summary), Evidence: visibleTexts(record.Evidence),
		Targets: []OutputTarget{}, RecordedAt: record.RecordedAt, Audit: []ObserverOutputAudit{},
	}
	for _, target := range record.Targets {
		row.Targets = append(row.Targets, OutputTarget{Source: visibleText(target.Source), ThreadID: visibleText(target.ThreadID)})
	}
	for key, consumer := range p.Consumers {
		identity := strings.Split(key, "\x00")
		if len(identity) != 3 {
			continue
		}
		observation, ok := consumer.Observations[record.ID]
		if !ok {
			continue
		}
		audit := ObserverOutputAudit{
			Consumer: visibleText(identity[0]), Source: visibleText(identity[1]), ThreadID: visibleText(identity[2]),
			ReadAt: observation.ReadAt, Outcome: visibleText(observation.Outcome), Reference: visibleText(observation.Reference),
		}
		if !observation.ConfirmedAt.IsZero() {
			at := observation.ConfirmedAt
			audit.ConfirmedAt = &at
		}
		row.Audit = append(row.Audit, audit)
	}
	sort.Slice(row.Audit, func(i, j int) bool {
		a, b := row.Audit[i], row.Audit[j]
		return a.Consumer+"\x00"+a.Source+"\x00"+a.ThreadID < b.Consumer+"\x00"+b.Source+"\x00"+b.ThreadID
	})
	return row
}

func (o *Observer) outputDetail(snapshot State, id, projectID string) (any, error) {
	if projectID == "" {
		return nil, operationError("invalid_argument", "observer_project_required", "Output detail requires its Project identity")
	}
	if err := o.triggerProject(projectID); err != nil {
		return nil, err
	}
	p := snapshot.Projects[projectID]
	record, ok := p.Outputs[id]
	if !ok {
		return nil, operationError("not_found", "observation_output_not_found", "Output was not found in this Project")
	}
	return struct {
		ObserverOutput
		ObservedAt time.Time `json:"observedAt"`
		Source     string    `json:"source"`
	}{observerOutput(p, record), time.Now().UTC(), "durable_mass_sa"}, nil
}

type observerOutputTraversal struct {
	Rows        []ObserverOutput
	Fingerprint [32]byte
	At          time.Time
}

type observerOutputCursor struct {
	ID          string   `json:"id"`
	After       string   `json:"after"`
	Fingerprint [32]byte `json:"filter"`
}

func (o *Observer) outputPage(snapshot State, query url.Values) (ObserverPage[ObserverOutput], error) {
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return ObserverPage[ObserverOutput]{}, operationError("invalid_argument", "invalid_observer_limit", "limit must be between 1 and 200")
		}
	}
	if project := query.Get("project"); project != "" {
		if err := o.triggerProject(project); err != nil {
			return ObserverPage[ObserverOutput]{}, err
		}
	}
	if category := query.Get("category"); category != "" && category != "reply" && category != "fact" && category != "other" {
		return ObserverPage[ObserverOutput]{}, operationError("invalid_argument", "invalid_output_category", "category must be reply, fact or other")
	}
	if query.Get("relatedTrigger") != "" && (query.Get("project") == "" || query.Get("trigger") != "" || query.Get("category") != "fact") {
		return ObserverPage[ObserverOutput]{}, operationError("invalid_argument", "invalid_related_output_query", "relatedTrigger requires Project and category=fact, without trigger")
	}
	filters := url.Values{}
	for key, values := range query {
		filters[key] = append([]string(nil), values...)
	}
	filters.Del("cursor")
	fingerprint := sha256.Sum256([]byte(filters.Encode()))
	now := time.Now().UTC()
	cursor := observerOutputCursor{Fingerprint: fingerprint}
	var traversal observerOutputTraversal
	start := 0
	if raw := query.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Fingerprint != fingerprint {
			return ObserverPage[ObserverOutput]{}, invalidTriggerCursor()
		}
		o.mu.Lock()
		traversal = o.outputPages[cursor.ID]
		o.mu.Unlock()
		if traversal.At.IsZero() || now.Sub(traversal.At) > 15*time.Minute || traversal.Fingerprint != fingerprint {
			return ObserverPage[ObserverOutput]{}, invalidTriggerCursor()
		}
		found := false
		for i, row := range traversal.Rows {
			if row.ProjectID+"\x00"+row.ID == cursor.After {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return ObserverPage[ObserverOutput]{}, invalidTriggerCursor()
		}
	} else {
		rows := []ObserverOutput{}
		var related TriggerState
		if id := query.Get("relatedTrigger"); id != "" {
			var ok bool
			related, ok = snapshot.Projects[query.Get("project")].Triggers[id]
			if !ok {
				return ObserverPage[ObserverOutput]{}, operationError("not_found", "observation_trigger_not_found", "Trigger was not found in this Project")
			}
		}
		for _, configured := range o.config.Projects {
			if project := query.Get("project"); project != "" && project != configured.ID {
				continue
			}
			p := snapshot.Projects[configured.ID]
			for _, record := range p.Outputs {
				if query.Get("trigger") != "" && record.TriggerID != query.Get("trigger") ||
					query.Get("ticket") != "" && record.TicketID != query.Get("ticket") ||
					query.Get("category") != "" && observerOutputCategory(record.Kind) != query.Get("category") {
					continue
				}
				// A linked Ticket alone does not broadcast its facts to every
				// Thread. Both the association and explicit destination must match.
				if related.TriggerID != "" && (!slices.Contains(related.TicketIDs, record.TicketID) || !outputMatches(record, related.Source, related.ThreadID)) {
					continue
				}
				targetMatches := query.Get("source") == "" && query.Get("thread") == ""
				for _, target := range record.Targets {
					if (query.Get("source") == "" || target.Source == query.Get("source")) && (query.Get("thread") == "" || target.ThreadID == query.Get("thread")) {
						targetMatches = true
					}
				}
				if targetMatches {
					rows = append(rows, observerOutput(p, record))
				}
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].ProjectID == rows[j].ProjectID {
				if rows[i].Sequence == rows[j].Sequence {
					return rows[i].ID < rows[j].ID
				}
				return rows[i].Sequence < rows[j].Sequence
			}
			return rows[i].ProjectID < rows[j].ProjectID
		})
		traversal = observerOutputTraversal{Rows: rows, Fingerprint: fingerprint, At: now}
	}
	end := min(start+limit, len(traversal.Rows))
	next := ""
	if end < len(traversal.Rows) {
		if cursor.ID == "" {
			var id [18]byte
			if _, err := rand.Read(id[:]); err != nil {
				return ObserverPage[ObserverOutput]{}, operationError("unavailable", "observer_cursor_unavailable", "cannot start traversal")
			}
			cursor.ID = base64.RawURLEncoding.EncodeToString(id[:])
			o.mu.Lock()
			if o.outputPages == nil {
				o.outputPages = map[string]observerOutputTraversal{}
			}
			for key, page := range o.outputPages {
				if now.Sub(page.At) > 15*time.Minute {
					delete(o.outputPages, key)
				}
			}
			if len(o.outputPages) >= 64 {
				oldest := ""
				var at time.Time
				for key, page := range o.outputPages {
					if oldest == "" || page.At.Before(at) {
						oldest, at = key, page.At
					}
				}
				delete(o.outputPages, oldest)
			}
			o.outputPages[cursor.ID] = traversal
			o.mu.Unlock()
		}
		cursor.After = traversal.Rows[end-1].ProjectID + "\x00" + traversal.Rows[end-1].ID
		data, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return ObserverPage[ObserverOutput]{Items: traversal.Rows[start:end], Next: next, ObservedAt: traversal.At, Source: "durable_mass_sa_traversal"}, nil
}
