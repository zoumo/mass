package codex

import (
	"encoding/json"

	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// The SDK also emits expired, unresolved foreign notifications after a
// diagnostic. A different thread ID alone never proves a child relationship.
// Only its explicit relationship observations admit descendant content.
func (s *session) projectEvents(native types.Event) []rt.Event {
	var child, parent string
	switch n := native.Data.(type) {
	case *types.SubagentAssociated:
		child, parent = n.ThreadID, n.ParentThreadID
	case *types.SubagentCreated:
		child, parent = n.ThreadID, n.ParentThreadID
	}
	if child != "" {
		if parent != s.id && !s.children[parent] {
			return []rt.Event{unresolvedSource(native)}
		}
		if s.children == nil {
			s.children = make(map[string]bool)
		}
		if !s.children[child] && len(s.children) >= 4096 {
			return []rt.Event{{Kind: rt.Failure, Error: &rt.SafeError{Category: "backpressure", Scope: "session"}}}
		}
		s.children[child] = true
	}
	var source struct{ ThreadID string }
	_ = json.Unmarshal(native.Params, &source)
	if source.ThreadID != "" && source.ThreadID != s.id && !s.children[source.ThreadID] {
		return []rt.Event{unresolvedSource(native)}
	}
	return attributedEvents(native, s.id)
}

func unresolvedSource(native types.Event) rt.Event {
	return rt.Event{Kind: rt.Unknown, Diagnostic: diagnostic("unresolved_source/"+native.Method, native.Params), Origin: rt.EventOrigin{Kind: "native", Attribution: "unresolved", Completeness: "unknown"}}
}
