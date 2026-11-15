package sa

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Targets are source/account scoped. Thread association alone is not a subscription.
type OutputTarget struct {
	Source   string `json:"source"`
	ThreadID string `json:"threadId"`
}

// OutputRecord is immutable content. Persisted content, consumer reads and
// channel outcomes have separate identities and never change Ticket execution.
type OutputRecord struct {
	ID         string         `json:"id"`
	Sequence   uint64         `json:"sequence"`
	ProjectID  string         `json:"projectId"`
	Kind       string         `json:"kind"`
	TriggerID  string         `json:"triggerId,omitempty"`
	TicketID   string         `json:"ticketId,omitempty"`
	FactID     string         `json:"factId,omitempty"`
	Summary    string         `json:"summary"`
	Evidence   []string       `json:"evidence,omitempty"`
	Targets    []OutputTarget `json:"targets"`
	RecordedAt time.Time      `json:"recordedAt"`
	RequestID  string         `json:"requestId,omitempty"`
}

type OutputObservation struct {
	ReadAt      time.Time `json:"readAt"`
	Outcome     string    `json:"outcome,omitempty"`
	Reference   string    `json:"reference,omitempty"`
	ConfirmedAt time.Time `json:"confirmedAt,omitempty"`
}

type OutputConsumer struct {
	Cursor       uint64                       `json:"cursor"`
	Observations map[string]OutputObservation `json:"observations"`
}

type OutputReadRequest struct {
	ProjectID string  `json:"projectId"`
	Consumer  string  `json:"consumer"`
	Source    string  `json:"source"`
	ThreadID  string  `json:"threadId"`
	After     *uint64 `json:"after,omitempty"`
	Limit     int     `json:"limit,omitempty"`
}

type ConsumedOutput struct {
	OutputRecord
	Observation OutputObservation `json:"observation"`
}

type OutputPage struct {
	Outputs    []ConsumedOutput `json:"outputs"`
	Next       uint64           `json:"next"`
	Checkpoint uint64           `json:"checkpoint"`
}

type OutputAckRequest struct {
	ProjectID string `json:"projectId"`
	Consumer  string `json:"consumer"`
	Source    string `json:"source"`
	ThreadID  string `json:"threadId"`
	OutputID  string `json:"outputId"`
	Outcome   string `json:"outcome"`
	Reference string `json:"reference"`
}

func cloneOutputs(in map[string]OutputRecord) map[string]OutputRecord {
	out := make(map[string]OutputRecord, len(in))
	for id, record := range in {
		record.Evidence = append([]string(nil), record.Evidence...)
		record.Targets = append([]OutputTarget(nil), record.Targets...)
		out[id] = record
	}
	return out
}

func cloneConsumers(in map[string]OutputConsumer) map[string]OutputConsumer {
	out := make(map[string]OutputConsumer, len(in))
	for id, consumer := range in {
		observations := make(map[string]OutputObservation, len(consumer.Observations))
		for key, observation := range consumer.Observations {
			observations[key] = observation
		}
		consumer.Observations = observations
		out[id] = consumer
	}
	return out
}

func appendOutput(p *ProjectState, record OutputRecord) OutputRecord {
	if old, ok := p.Outputs[record.ID]; ok {
		return old
	}
	p.NextOutputSeq++
	record.Sequence = p.NextOutputSeq
	p.Outputs[record.ID] = record
	return record
}

func validateConsumer(consumer, source, thread string) error {
	for name, value := range map[string]string{"consumer": consumer, "source": source, "threadId": thread} {
		if err := validateInboxField(name, value, true); err != nil {
			return err
		}
		if strings.ContainsRune(value, '\x00') {
			return operationError("invalid_argument", "invalid_output_identity", "output identities must not contain NUL")
		}
	}
	return nil
}

func consumerKey(consumer, source, thread string) string {
	return consumer + "\x00" + threadKey(source, thread)
}

func outputMatches(record OutputRecord, source, thread string) bool {
	return slices.Contains(record.Targets, OutputTarget{Source: source, ThreadID: thread})
}

func orderedOutputs(p *ProjectState) []OutputRecord {
	records := make([]OutputRecord, 0, len(p.Outputs))
	for _, record := range p.Outputs {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Sequence < records[j].Sequence })
	return records
}

// ReadOutputs records observation only. Without after it resumes the durable
// contiguous checkpoint. Explicit after enables audit and reconciliation reads.
func (s *WorkerService) ReadOutputs(req OutputReadRequest) (OutputPage, error) {
	if _, err := s.project(req.ProjectID); err != nil {
		return OutputPage{}, err
	}
	if err := validateConsumer(req.Consumer, req.Source, req.ThreadID); err != nil {
		return OutputPage{}, err
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit < 1 || req.Limit > 1000 {
		return OutputPage{}, operationError("invalid_argument", "invalid_output_limit", "limit must be 1 through 1000")
	}
	result := OutputPage{Outputs: []ConsumedOutput{}}
	err := s.store.updateProject(req.ProjectID, func(p *ProjectState) error {
		key := consumerKey(req.Consumer, req.Source, req.ThreadID)
		consumer := p.Consumers[key]
		if consumer.Observations == nil {
			consumer.Observations = map[string]OutputObservation{}
		}
		after := consumer.Cursor
		if req.After != nil {
			after = *req.After
			if after > p.NextOutputSeq {
				return operationError("invalid_argument", "invalid_output_cursor", "cursor exceeds the recorded output sequence")
			}
		}
		result.Next, result.Checkpoint = after, consumer.Cursor
		for _, record := range orderedOutputs(p) {
			if record.Sequence <= after || !outputMatches(record, req.Source, req.ThreadID) {
				continue
			}
			observation := consumer.Observations[record.ID]
			if observation.ReadAt.IsZero() {
				observation.ReadAt = s.now()
			}
			consumer.Observations[record.ID] = observation
			result.Outputs = append(result.Outputs, ConsumedOutput{record, observation})
			result.Next = record.Sequence
			if len(result.Outputs) == req.Limit {
				break
			}
		}
		p.Consumers[key] = consumer
		return nil
	})
	return result, err
}

// Acknowledgements consume all three outcomes. failed/unknown therefore never
// cause automatic resend. Explicit audit can reconcile either to delivered.
func (s *WorkerService) AckOutput(req OutputAckRequest) (OutputConsumer, error) {
	if _, err := s.project(req.ProjectID); err != nil {
		return OutputConsumer{}, err
	}
	if err := validateConsumer(req.Consumer, req.Source, req.ThreadID); err != nil {
		return OutputConsumer{}, err
	}
	if req.Outcome != "delivered" && req.Outcome != "failed" && req.Outcome != "unknown" {
		return OutputConsumer{}, operationError("invalid_argument", "invalid_channel_outcome", "outcome must be delivered, failed, or unknown")
	}
	if err := validateBlockText("channel reference or reason", req.Reference, true); err != nil {
		return OutputConsumer{}, err
	}
	var result OutputConsumer
	err := s.store.updateProject(req.ProjectID, func(p *ProjectState) error {
		record, ok := p.Outputs[req.OutputID]
		if !ok || !outputMatches(record, req.Source, req.ThreadID) {
			return workerNotFound()
		}
		key := consumerKey(req.Consumer, req.Source, req.ThreadID)
		consumer := p.Consumers[key]
		observation := consumer.Observations[record.ID]
		if observation.ReadAt.IsZero() {
			return operationError("failed_precondition", "output_not_read", "consumer must read this authorized output before confirming it")
		}
		if observation.Outcome != "" {
			if observation.Outcome == req.Outcome && observation.Reference == req.Reference {
				result = consumer
				return nil
			}
			if observation.Outcome == "delivered" || req.Outcome != "delivered" {
				return operationError("failed_precondition", "channel_outcome_conflict", "confirmed outcome cannot be retried; reconcile failed or unknown explicitly to delivered")
			}
		}
		observation.Outcome, observation.Reference, observation.ConfirmedAt = req.Outcome, req.Reference, s.now()
		consumer.Observations[record.ID] = observation
		for _, candidate := range orderedOutputs(p) {
			if candidate.Sequence <= consumer.Cursor || !outputMatches(candidate, req.Source, req.ThreadID) {
				continue
			}
			if consumer.Observations[candidate.ID].Outcome == "" {
				break
			}
			consumer.Cursor = candidate.Sequence
		}
		p.Consumers[key], result = consumer, consumer
		return nil
	})
	return result, err
}

type replyTriggerArgs struct {
	TriggerID string `json:"triggerId"`
	RequestID string `json:"requestId"`
	Kind      string `json:"kind"`
	TicketID  string `json:"ticketId,omitempty"`
	FactID    string `json:"factId,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (s *WorkerService) replyTrigger(projectID string, args replyTriggerArgs) (OutputRecord, error) {
	if err := validateSingleLine("requestId", args.RequestID, true); err != nil {
		return OutputRecord{}, err
	}
	if args.Kind != "accepted" && args.Kind != "clarification" && args.Kind != "no_execution" && args.Kind != "summary" {
		return OutputRecord{}, operationError("invalid_argument", "invalid_reply_kind", "kind must be accepted, clarification, no_execution, or summary")
	}
	if args.Kind == "accepted" {
		if args.Text != "" {
			return OutputRecord{}, operationError("invalid_argument", "acceptance_text_forbidden", "acceptance content is generated from confirmed Ticket facts")
		}
	} else if err := validateBlockText("reply text", args.Text, true); err != nil {
		return OutputRecord{}, err
	}
	var result OutputRecord
	err := s.store.updateProject(projectID, func(p *ProjectState) error {
		trigger, ok := p.Triggers[args.TriggerID]
		if !ok {
			return workerNotFound()
		}
		id := "reply/" + args.TriggerID + "/" + args.RequestID
		text := args.Text
		if args.TicketID != "" && !slices.Contains(trigger.TicketIDs, args.TicketID) {
			return workerNotFound()
		}
		if args.Kind == "accepted" {
			if args.TicketID == "" || trigger.Decision != "created" && trigger.Decision != "associated" {
				return operationError("failed_precondition", "ticket_not_confirmed", "confirm Ticket creation or association before accepting work")
			}
			text = fmt.Sprintf("Request associated with confirmed Ticket %s. Execution and completion are not yet confirmed by this acknowledgement.", args.TicketID)
		}
		if args.Kind == "summary" || args.FactID != "" {
			fact, ok := p.Outputs[args.FactID]
			if !ok || !strings.HasPrefix(fact.Kind, "ticket.") || fact.TicketID != args.TicketID || args.TicketID == "" {
				return workerNotFound()
			}
		}
		if old, ok := p.Outputs[id]; ok {
			if old.Kind != "reply."+args.Kind || old.TicketID != args.TicketID || old.FactID != args.FactID || old.Summary != text {
				return operationError("already_exists", "reply_identity_conflict", "reply request identity has different content")
			}
			result = old
			return nil
		}
		result = appendOutput(p, OutputRecord{ID: id, RequestID: args.RequestID, ProjectID: projectID, Kind: "reply." + args.Kind, TriggerID: args.TriggerID, TicketID: args.TicketID, FactID: args.FactID, Summary: text, Targets: []OutputTarget{{Source: trigger.Source, ThreadID: trigger.ThreadID}}, RecordedAt: s.now()})
		return nil
	})
	return result, err
}

type subscribeOutputArgs struct {
	TicketID  string `json:"ticketId"`
	TriggerID string `json:"triggerId"`
}

func (s *WorkerService) subscribeOutput(projectID string, args subscribeOutputArgs) (TicketState, error) {
	var result TicketState
	err := s.store.updateProject(projectID, func(p *ProjectState) error {
		trigger, ok := p.Triggers[args.TriggerID]
		ticket, exists := p.Tickets[args.TicketID]
		if !ok || !exists || !slices.Contains(trigger.TicketIDs, ticket.ID) {
			return workerNotFound()
		}
		target := OutputTarget{Source: trigger.Source, ThreadID: trigger.ThreadID}
		if !slices.Contains(ticket.OutputSubscribers, target) {
			ticket.OutputSubscribers = append(ticket.OutputSubscribers, target)
		}
		p.Tickets[ticket.ID], result = ticket, ticket
		return nil
	})
	return result, err
}

// Called in the same transaction that confirms the business transition.
func recordTicketFact(p *ProjectState, ticket TicketState, transition TicketTransition, now time.Time) OutputRecord {
	targets := append([]OutputTarget(nil), ticket.OutputSubscribers...)
	trigger := p.Triggers[ticket.OutputTriggerID]
	if trigger.TriggerID != "" {
		target := OutputTarget{Source: trigger.Source, ThreadID: trigger.ThreadID}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	evidence := []string{"multica:issue:" + ticket.Issue.ID, "multica:comment:" + transition.CommentID}
	for _, report := range ticket.Reports {
		if report.AssignmentID == ticket.AssignmentID && report.Status == "confirmed" {
			evidence = append(evidence, "multica:comment:"+report.CommentID)
		}
	}
	return appendOutput(p, OutputRecord{ID: "fact/" + ticket.ID + "/" + strconv.FormatUint(ticket.Revision, 10), ProjectID: ticket.ProjectID, Kind: "ticket." + transition.State, TicketID: ticket.ID, TriggerID: ticket.OutputTriggerID, Summary: transition.Summary, Evidence: evidence, Targets: targets, RecordedAt: now})
}
