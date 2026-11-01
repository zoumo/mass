package claude

import "github.com/zoumo/mass/mass-go/pkg/runtime"

type messageClaim struct {
	work    string
	started bool
}

// Retain tombstones instead of evicting: an unknown or reused native ID must
// never acquire a new owner later in this process. Only message_start proves
// a link for assistant frames that omit the native caller UUID.
func (s *session) linkMessage(id string, start, trusted bool, explicit *string) error {
	if id == "" {
		return nil
	}
	if s.messages == nil {
		s.messages = map[string]messageClaim{}
	}
	claim, seen := s.messages[id]
	if !seen {
		if len(s.messages) == 128 {
			return runtime.Exhausted("native_message_linkage_capacity")
		}
		if trusted && s.active && s.observationWork == s.work {
			claim = messageClaim{work: s.work, started: start}
		}
		s.messages[id] = claim
		return nil
	}
	if start || !trusted || claim.work == "" ||
		(explicit != nil && *explicit != "" && *explicit != claim.work) ||
		(s.observationWork != "" && s.observationWork != claim.work) {
		s.messages[id] = messageClaim{}
		s.observationWork, s.attribution = "", "unknown"
		return nil
	}
	if explicit == nil || *explicit == "" {
		if claim.started && s.active && claim.work == s.work {
			s.observationWork, s.attribution = claim.work, "native_message_id"
		}
	}
	return nil
}

func (s *session) linkPreview(trusted bool, explicit *string) {
	claim := s.messages[s.messageID]
	if !trusted || claim.work == "" || !claim.started || !s.active || claim.work != s.work {
		s.observationWork, s.attribution = "", "unknown"
		return
	}
	if explicit != nil && *explicit != "" {
		if *explicit != claim.work {
			s.messages[s.messageID] = messageClaim{}
			s.observationWork, s.attribution = "", "unknown"
		}
		return
	}
	s.observationWork, s.attribution = claim.work, "native_message_id"
}
