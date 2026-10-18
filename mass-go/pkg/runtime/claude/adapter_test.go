package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type runtimeSuite struct {
	suite.Suite
	executable string
}

func TestRuntime(t *testing.T) { suite.Run(t, new(runtimeSuite)) }
func (s *runtimeSuite) SetupSuite() {
	s.executable = filepath.Join(s.T().TempDir(), "mock-claude")
	output, err := exec.Command("go", "build", "-o", s.executable, "github.com/zoumo/agent-sdk-go/claude/cmd/mock").CombinedOutput()
	s.Require().NoError(err, string(output))
}
func startup() []string {
	return []string{
		`client control {"type":"control_request","request_id":"init","request":{"subtype":"initialize","hooks":null,"forwardSubagentText":true}}`,
		`server control {"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}`,
	}
}
func send() string {
	return `client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`
}
func (s *runtimeSuite) open(frames []string) (rt.Runtime, rt.Session, context.Context) {
	s.T().Helper()
	var native []map[string]any
	for i, frame := range frames {
		direction, rest, _ := strings.Cut(frame, " ")
		channel, data, _ := strings.Cut(rest, " ")
		native = append(native, map[string]any{"ordinal": i + 1, "direction": direction, "channel": channel, "data": json.RawMessage(data)})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "provenance": "synthetic", "frames": native})
	s.Require().NoError(err)
	path := filepath.Join(s.T().TempDir(), "scenario.json")
	s.Require().NoError(os.WriteFile(path, data, 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	s.T().Cleanup(cancel)
	registry := catalog.New()
	_, err = registry.Register(rt.Registration{Name: "test-runtime", Type: "claude", Config: rt.Config{Executable: s.executable, Env: []string{"CLAUDE_MOCK_SCENARIO=" + path}}})
	s.Require().NoError(err)
	conn, err := registry.Get("test-runtime")
	s.Require().NoError(err)
	err = conn.Start(ctx)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(conn.Stop()) })
	session, err := conn.CreateSession(ctx)
	s.Require().NoError(err)
	return conn, session, ctx
}
func (s *runtimeSuite) next(session rt.Session, ctx context.Context) rt.Event {
	s.T().Helper()
	for {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Kind != rt.Unknown {
			return event
		}
	}
}
func (s *runtimeSuite) TestMessagesToolsAndNativeCompletion() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"native-session"}`,
		`server protocol {"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"hello back"},{"type":"tool_use","id":"tool-1","name":"Read","input":{}}]}}`,
		`server protocol {"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"text","text":"synthetic "},{"type":"text","text":"output"}],"is_error":false}]}}`,
		`server protocol {"type":"result","subtype":"success","session_id":"native-session","is_error":false,"result":"done"}`)
	conn, session, ctx := s.open(frames)
	s.Equal(rt.SessionKey{RuntimeName: "test-runtime"}, session.Key())
	s.Equal(rt.Capabilities{}, session.Capabilities())
	submission, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	s.Equal(rt.Submission{Evidence: rt.SDKWriteSuccess}, submission)
	event := s.next(session, ctx)
	s.Equal(rt.SessionReady, event.Kind)
	s.Equal("native-session", event.SessionID)
	s.Equal(rt.SessionKey{RuntimeName: "test-runtime", SessionID: "native-session"}, session.Key())
	event = s.next(session, ctx)
	s.Equal(rt.Message, event.Kind)
	s.Equal("hello back", event.Text)
	event = s.next(session, ctx)
	s.Equal(rt.Tool, event.Kind)
	s.Equal("tool-1", event.ItemID)
	s.Equal("Read", event.Tool)
	event = s.next(session, ctx)
	s.Equal(rt.Tool, event.Kind)
	s.Equal("synthetic output", event.Text)
	event = s.next(session, ctx)
	s.Equal(rt.TurnCompleted, event.Kind)
	s.Empty(event.TurnID)
	s.Equal("completed", event.Status)
	s.NoError(conn.Stop())
}
func (s *runtimeSuite) TestInteractionsAndInterrupt() {
	for _, tc := range []struct {
		name, tool, input, answer string
		response                  rt.Response
	}{
		{"permission", "Read", `{}`, `{"behavior":"allow","updatedInput":{}}`, rt.Response{Allow: true}},
		{"question", "AskUserQuestion", `{"questions":[{"question":"Which?","options":[{"label":"A"}]}]}`, `{"behavior":"allow","updatedInput":{"questions":[{"question":"Which?","options":[{"label":"A"}]}],"answers":{"Which?":"A"}}}`, rt.Response{Allow: true, Answers: map[string][]string{"Which?": {"A"}}}},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), send(), `server control {"type":"control_request","request_id":"original-request","request":{"subtype":"can_use_tool","tool_name":"`+tc.tool+`","tool_use_id":"tool-1","input":`+tc.input+`}}`,
				`client control {"type":"control_response","response":{"subtype":"success","request_id":"original-request","response":`+tc.answer+`}}`,
				`client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`,
				`server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`,
				`server protocol {"type":"result","subtype":"error_during_execution","session_id":"native-session","is_error":true}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			event := s.next(session, ctx)
			s.Require().Equal(rt.InteractionRequested, event.Kind)
			handle := event.Interaction.Handle
			s.Equal("original-request", handle.ID())
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			s.ErrorIs(handle.Respond(canceled, tc.response), context.Canceled)
			s.NoError(handle.Err())
			s.Require().NoError(handle.Respond(ctx, tc.response))
			s.Error(handle.Respond(ctx, tc.response))
			s.Error(handle.Err())
			s.Error(session.Cancel(ctx, "invented-turn"))
			s.Require().NoError(session.Cancel(ctx, ""))
			event = s.next(session, ctx)
			s.Equal(rt.TurnCompleted, event.Kind)
			s.Equal("failed", event.Status)
		})
	}
}
func (s *runtimeSuite) TestCloseWithoutConsuming() {
	original := s.executable
	wrapper, exited := testutil.TrackProcess(s.T(), original)
	s.executable = wrapper
	defer func() { s.executable = original }()
	frames := append(startup(), send(), `server control {"type":"control_request","request_id":"pending","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`, `client control {"type":"control_response","response":{"subtype":"success","request_id":"pending","response":{"behavior":"deny","message":""}}}`)
	conn, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	s.NoError(conn.Stop())
	s.NoError(session.Close())
	exited()
}

func (s *runtimeSuite) TestChildFactsDoNotOwnRoot() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"root"}`,
		`server protocol {"type":"result","subtype":"success","session_id":"child","agent_id":"child-agent","parent_tool_use_id":"spawn","is_error":false}`,
		`server control {"type":"control_request","request_id":"child-permission","request":{"subtype":"can_use_tool","tool_name":"Read","agent_id":"child-agent","tool_use_id":"child-tool","input":{}}}`,
		`client control {"type":"control_response","response":{"subtype":"success","request_id":"child-permission","response":{"behavior":"deny","message":""}}}`)
	conn, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	s.Equal(rt.SessionReady, s.next(session, ctx).Kind)
	seen := map[string]bool{}
	for len(seen) < 2 {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Diagnostic.NativeKind == "result/success" || event.Diagnostic.NativeKind == "can_use_tool/unowned" {
			seen[event.Diagnostic.NativeKind] = true
			s.Equal(rt.Unknown, event.Kind)
			s.Nil(event.Interaction)
		}
	}
	s.Equal("root", session.Key().SessionID)
	s.NoError(conn.Stop())
}

func (s *runtimeSuite) TestPendingHandleExpiresOnClose() {
	frames := append(startup(), send(), `server control {"type":"control_request","request_id":"pending","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`, `client control {"type":"control_response","response":{"subtype":"success","request_id":"pending","response":{"behavior":"deny","message":""}}}`)
	conn, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	event := s.next(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	handle := event.Interaction.Handle
	s.NoError(conn.Stop())
	select {
	case <-handle.Done():
	case <-ctx.Done():
		s.FailNow("closed connection left interaction live")
	}
	s.Error(handle.Err())
	s.Error(handle.Respond(ctx, rt.Response{Allow: true}))
}
func (s *runtimeSuite) TestUnknownAndNativeErrorDiagnostics() {
	for _, tc := range []struct {
		name, frame string
		kind        rt.EventKind
		nativeKind  string
	}{
		{"unknown", `{"type":"future_kind","private":"synthetic-sensitive-marker"}`, rt.Unknown, "future_kind/"},
		{"error", `{"type":"assistant","error":"synthetic-sensitive-marker","message":{"role":"assistant","content":[]}}`, rt.Failure, "assistant/"},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), "server protocol "+tc.frame))
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			for {
				event, err := session.Next(ctx)
				s.Require().NoError(err)
				if event.Diagnostic.NativeKind == tc.nativeKind {
					s.Equal(tc.kind, event.Kind)
					s.Len(event.Diagnostic.SHA256, 64)
					s.NotContains(event.Text, "synthetic-sensitive-marker")
					break
				}
			}
		})
	}
}

func (s *runtimeSuite) TestNativeCancellationInvalidatesHandle() {
	frames := append(startup(), send(),
		`server control {"type":"control_request","request_id":"cancelled","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`,
		`client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`,
		`server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`,
		`server control {"type":"control_cancel_request","request_id":"cancelled"}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	event := s.next(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	handle := event.Interaction.Handle
	s.Require().NoError(session.Cancel(ctx, ""))
	select {
	case <-handle.Done():
	case <-ctx.Done():
		s.FailNow("native cancellation left handle live")
	}
	s.Error(handle.Err())
	s.Error(handle.Respond(ctx, rt.Response{Allow: true}))
}
