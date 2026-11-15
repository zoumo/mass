package sa

import "sort"

type ticketContextArgs struct {
	TicketID string `json:"ticketId"`
}

type SessionHistoryReference struct {
	AssignmentID string     `json:"assignmentId"`
	Session      SessionRef `json:"session"`
	Status       string     `json:"status"`
}

type ThreadReference struct {
	Source     string   `json:"source"`
	ThreadID   string   `json:"threadId"`
	TriggerIDs []string `json:"triggerIds,omitempty"`
}

type TicketContext struct {
	ChildInputs []InboxEventState         `json:"childInputs"`
	Facts       []OutputRecord            `json:"facts"`
	Ticket      TicketState               `json:"ticket"`
	Threads     []ThreadReference         `json:"threads"`
	Sessions    []SessionHistoryReference `json:"sessions"`
	Parent      *TicketState              `json:"parent,omitempty"`
	Children    []TicketState             `json:"children"`
}

// Context returns business summaries and explicit history references. It does
// not fetch or broadcast transcripts. Shared Thread membership grants no
// access to another root's Ticket, Trigger payloads, or Session histories.
func (s *WorkerService) ticketContext(cap CapabilityState, ticketID string) (TicketContext, error) {
	p := s.store.Snapshot().Projects[cap.ProjectID]
	if cap.Role == "worker" && ticketID == "" {
		ticketID = p.Assignments[cap.AssignmentID].TicketID
	}
	ticket, ok := p.Tickets[ticketID]
	if !ok || ticket.ProjectID != cap.ProjectID {
		return TicketContext{}, workerNotFound()
	}
	if cap.Role == "worker" && ticket.RootTicketID != cap.RootTicketID {
		return TicketContext{}, workerNotFound()
	}
	result := TicketContext{Ticket: ticket, Threads: []ThreadReference{}, Sessions: []SessionHistoryReference{}, Children: []TicketState{}}
	for _, input := range p.Inbox {
		if input.TicketID == ticket.ID && input.ChildLifecycle != nil {
			result.ChildInputs = append(result.ChildInputs, cloneInboxEvent(input))
		}
	}
	sortInboxEvents(result.ChildInputs)
	for _, record := range p.Outputs {
		if record.TicketID == ticket.ID && len(record.Kind) >= 7 && record.Kind[:7] == "ticket." {
			result.Facts = append(result.Facts, record)
		}
	}
	sort.Slice(result.Facts, func(i, j int) bool { return result.Facts[i].Sequence < result.Facts[j].Sequence })
	for _, thread := range p.Threads {
		linked := false
		for _, id := range thread.TicketIDs {
			if id == ticket.ID {
				linked = true
			}
		}
		if !linked {
			continue
		}
		ref := ThreadReference{Source: thread.Source, ThreadID: thread.ThreadID}
		for _, trigger := range p.Triggers {
			if trigger.Source != thread.Source || trigger.ThreadID != thread.ThreadID {
				continue
			}
			for _, id := range trigger.TicketIDs {
				if id == ticket.ID {
					ref.TriggerIDs = append(ref.TriggerIDs, trigger.TriggerID)
					break
				}
			}
		}
		sort.Strings(ref.TriggerIDs)
		result.Threads = append(result.Threads, ref)
	}
	for _, a := range p.Assignments {
		if a.TicketID == ticket.ID {
			result.Sessions = append(result.Sessions, SessionHistoryReference{a.ID, a.Session, a.Status})
		}
	}
	for _, other := range p.Tickets {
		if other.ProjectID != cap.ProjectID || other.RootTicketID != ticket.RootTicketID {
			continue
		}
		if other.ID == ticket.ParentTicketID {
			parent := other
			result.Parent = &parent
		}
		if other.ParentTicketID == ticket.ID {
			result.Children = append(result.Children, other)
		}
	}
	sort.Slice(result.Threads, func(i, j int) bool {
		return threadKey(result.Threads[i].Source, result.Threads[i].ThreadID) < threadKey(result.Threads[j].Source, result.Threads[j].ThreadID)
	})
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].AssignmentID < result.Sessions[j].AssignmentID })
	sort.Slice(result.Children, func(i, j int) bool { return result.Children[i].ID < result.Children[j].ID })
	return result, nil
}
