package service

import (
	"context"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

const (
	legacyArchivedState     = events.SessionState("archived")
	restartStateUnconfirmed = "state_unconfirmed_after_restart"
)

// Prepare corrects persisted run state before the application opens business
// admission. It performs no Runtime or Session control operations.
func (s *Service) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.startup.Lock()
	defer s.startup.Unlock()

	runtimes, err := s.meta.ListRuntimes(resource.RuntimeQuery{})
	if err != nil {
		return err
	}
	for _, r := range runtimes {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch r.Status.State {
		case "stopped", "unknown":
			continue
		default:
			if _, err := s.runtimeStatus(r.Metadata.Name, func(status *resource.RuntimeStatus) {
				status.State = "unknown"
			}); err != nil {
				return err
			}
		}
	}

	query := resource.SessionQuery{Limit: queueCapacity}
	for {
		page, err := s.meta.ListSessions(query)
		if err != nil {
			return err
		}
		for _, r := range page.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			unresolved := unresolvedHistoryLoad(r)
			if r.Status.State == "offline" && !unresolved || r.Status.State == events.Unknown {
				continue
			}
			if r.Status.State == legacyArchivedState && !unresolved {
				if _, err := s.sessionStatus(resource.SessionRef{SessionID: r.Status.SessionID}, func(session *resource.Session) {
					offlineStatus(session)
				}); err != nil {
					return err
				}
				continue
			}
			// Legacy archive refused to accept an unresolved history load. If
			// persisted data violates that invariant, do not claim its native
			// responsibility was released; retain the more conservative unknown.
			if _, err := s.sessionStatus(resource.SessionRef{SessionID: r.Status.SessionID}, func(session *resource.Session) {
				session.Status.State = events.Unknown
				session.Status.Reason = restartStateUnconfirmed
			}); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		query.After = page.Next
	}
}
