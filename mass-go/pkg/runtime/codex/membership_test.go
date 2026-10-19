package codex

import (
	"encoding/json"
	"testing"

	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestUnprovenForeignThreadCannotBecomeChildContent(t *testing.T) {
	s := &session{id: "root"}
	message := types.Event{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"foreign","turnId":"work","itemId":"message","delta":"foreign private content"}`)}
	e := s.projectEvents(message)[0]
	if e.Kind != rt.Unknown || e.Message != nil || e.Subject != nil || e.Origin.Attribution != "unresolved" {
		t.Fatalf("unproven source exposed: %+v", e)
	}
	// An association to another root still grants no membership here.
	s.projectEvents(types.Event{Data: &types.SubagentAssociated{ThreadID: "foreign", ParentThreadID: "other-root"}})
	if s.projectEvents(message)[0].Kind != rt.Unknown {
		t.Fatal("foreign relation admitted")
	}
	// Once the SDK proves the exact relationship, preserve real child content.
	s.projectEvents(types.Event{Data: &types.SubagentAssociated{ThreadID: "foreign", ParentThreadID: "root"}})
	e = s.projectEvents(message)[0]
	if e.Message == nil || e.Message.Stage != "delta" || e.Origin.Attribution != "child" || e.Subject == nil || e.Subject.AgentID != "foreign" || e.WorkRef != "" {
		t.Fatalf("proven child lost: %+v", e)
	}
	s.projectEvents(types.Event{Data: &types.SubagentAssociated{ThreadID: "grandchild", ParentThreadID: "foreign"}})
	if !s.children["grandchild"] {
		t.Fatal("proven recursive relationship lost")
	}
}
