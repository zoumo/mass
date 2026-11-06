package integration_test

import (
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *entrypointSuite) TestCodexCloseDrainsFinalFact()  { s.ticket04Drain("codex", false) }
func (s *entrypointSuite) TestClaudeCloseDrainsFinalFact() { s.ticket04Drain("claude", false) }
func (s *entrypointSuite) TestCodexStopDrainsFinalFact()   { s.ticket04Drain("codex", true) }
func (s *entrypointSuite) TestClaudeStopDrainsFinalFact()  { s.ticket04Drain("claude", true) }

func (s *entrypointSuite) ticket04Drain(kind string, stop bool) {
	mode := "ticket04-close-drain"
	expectedFact := "final-close-fact"
	if stop {
		mode = "ticket04-drain"
		expectedFact = "final-cancellation-fact"
	}
	ref, _, _ := s.open(kind, mode)
	_, scan := s.observer(ref)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}}, nil)
	running := false
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Running {
			running = true
			break
		}
	}
	s.Require().True(running, "native work must be executing before lifecycle control")
	if stop {
		s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
	} else {
		s.call("session.close", workflowParams{Session: ref}, nil)
		var session resource.Session
		s.call("session.get", workflowParams{Session: ref}, &session)
		s.EqualValues("offline", session.Status.State)
	}
	// A successful lifecycle return must already include the final append and
	// required publication. Match the same fact through SSE and public history.
	page := s.history(ref)
	finalID := ""
	for _, e := range page.Events {
		if p, ok := e.Payload().(events.AgentMessage); ok && p.MessageID == expectedFact {
			finalID = e.EventID
		}
	}
	s.Require().NotEmpty(finalID)
	published := false
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if e.EventID == finalID {
			published = true
			break
		}
	}
	s.Require().True(published, "the committed final fact must reach the required SSE observer")
	if !stop {
		s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
	}
}
