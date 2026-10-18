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
	for i, frame := range frames {
		if frame == send() {
			id := "native-session"
			for _, f := range frames {
				if strings.Contains(f, `"session_id":"root"`) {
					id = "root"
				}
			}
			if i+1 < len(frames) && strings.Contains(frames[i+1], `"subtype":"init"`) {
				break
			}
			init := `server protocol {"type":"system","subtype":"init","session_id":"` + id + `"}`
			frames = append(frames[:i+1:i+1], append([]string{init}, frames[i+1:]...)...)
			break
		}
	}
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
	input := "hello"
	session := testutil.OpenSession(s.T(), conn, ctx, rt.SessionOptions{InitialInput: &input})
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
func (s *runtimeSuite) nextInteraction(session rt.Session, ctx context.Context) rt.Event {
	s.T().Helper()
	for {
		event := s.next(session, ctx)
		if event.Kind == rt.InteractionRequested {
			return event
		}
		s.Require().Equal(rt.SessionReady, event.Kind)
	}
}
func (s *runtimeSuite) TestMessagesToolsAndNativeCompletion() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"native-session"}`,
		`server protocol {"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"hello back"},{"type":"tool_use","id":"tool-1","name":"Read","input":{}}]}}`,
		`server protocol {"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"text","text":"synthetic "},{"type":"text","text":"output"}],"is_error":false}]}}`,
		`server protocol {"type":"result","subtype":"success","session_id":"native-session","is_error":false,"result":"done"}`)
	conn, session, ctx := s.open(frames)
	s.Equal("native-session", session.Key().SessionID)
	s.Equal(rt.Capabilities{VerifiedResume: true}, session.Capabilities())
	event := s.next(session, ctx)
	s.Equal(rt.SessionReady, event.Kind)
	s.Equal("native-session", event.SessionID)
	s.Equal(rt.SessionKey{RuntimeName: "test-runtime", SessionID: "native-session"}, session.Key())
	event = s.next(session, ctx)
	s.Equal(rt.MessageCompleted, event.Kind)
	s.Equal("hello back", testutil.MessageText(event))
	event = s.next(session, ctx)
	s.Equal(rt.ToolAnnounced, event.Kind)
	s.Equal("tool-1", event.Tool.CallRef)
	s.Equal("Read", event.Tool.Name)
	event = s.next(session, ctx)
	s.Equal(rt.MessageCompleted, event.Kind) // User message retains role and tool-result block.
	event = s.next(session, ctx)
	s.Equal(rt.ToolCompleted, event.Kind)
	s.Require().Len(event.Tool.Output.Blocks, 2)
	s.Equal("synthetic ", *event.Tool.Output.Blocks[0].Text)
	s.Equal("output", *event.Tool.Output.Blocks[1].Text)
	event = s.next(session, ctx)
	s.Equal(rt.TurnCompleted, event.Kind)
	s.Empty(event.WorkRef)
	s.Equal("completed", event.Work.Status)
	s.NoError(conn.Stop())
}
func (s *runtimeSuite) TestInteractionsAndInterrupt() {
	for _, tc := range []struct {
		name, tool, input, answer string
		response                  rt.Response
	}{
		{"permission", "Read", `{}`, `{"behavior":"allow","updatedInput":{}}`, rt.Response{Decision: "allow_once"}},
		{"question", "AskUserQuestion", `{"questions":[{"question":"Which?","options":[{"label":"A"}]}]}`, `{"behavior":"allow","updatedInput":{"questions":[{"question":"Which?","options":[{"label":"A"}]}],"answers":{"Which?":"A"}}}`, rt.Response{Answers: map[string]rt.Answer{"Which?": {Selections: []string{"A"}}}}},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), send(), `server control {"type":"control_request","request_id":"original-request","request":{"subtype":"can_use_tool","tool_name":"`+tc.tool+`","tool_use_id":"tool-1","input":`+tc.input+`}}`,
				`client control {"type":"control_response","response":{"subtype":"success","request_id":"original-request","response":`+tc.answer+`}}`,
				`client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`,
				`server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`,
				`server protocol {"type":"result","subtype":"error_during_execution","session_id":"native-session","is_error":true}`)
			_, session, ctx := s.open(frames)
			event := s.nextInteraction(session, ctx)
			s.Require().Equal(rt.InteractionRequested, event.Kind)
			ref := event.Interaction.Ref
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, responseErr := session.Respond(canceled, ref, tc.response)
			s.ErrorIs(responseErr, context.Canceled)
			receipt, responseErr := session.Respond(ctx, ref, tc.response)
			s.Require().NoError(responseErr)
			s.Equal(rt.Known(true), receipt.WriteCompleted)
			s.Equal("unknown", receipt.NativeResolution.State)
			_, responseErr = session.Respond(ctx, ref, tc.response)
			s.Error(responseErr)
			_, cancelErr := session.Cancel(ctx, "invented-turn")
			s.ErrorIs(cancelErr, rt.ErrNotFound)
			_, cancelErr = session.Cancel(ctx, session.Snapshot().Foreground.OperationRef)
			s.Require().NoError(cancelErr)
			event = s.next(session, ctx)
			for event.Kind == rt.SessionReady {
				event = s.next(session, ctx)
			}
			s.Require().Equal(rt.TurnCompleted, event.Kind)
			s.Equal("failed", event.Work.Status)
		})
	}
}
func (s *runtimeSuite) TestCloseWithoutConsuming() {
	original := s.executable
	wrapper, exited := testutil.TrackProcess(s.T(), original)
	s.executable = wrapper
	defer func() { s.executable = original }()
	frames := append(startup(), send(), `server control {"type":"control_request","request_id":"pending","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`, `client control {"type":"control_response","response":{"subtype":"success","request_id":"pending","response":{"behavior":"deny","message":""}}}`)
	conn, session, _ := s.open(frames)
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
	s.Equal(rt.SessionReady, s.next(session, ctx).Kind)
	seen := map[string]bool{}
	for len(seen) < 2 {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Diagnostic.NativeKind == "result/success" || event.Diagnostic.NativeKind == "control_request/" {
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
	event := s.nextInteraction(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	ref := event.Interaction.Ref
	s.NoError(conn.Stop())
	receipt, responseErr := session.Respond(ctx, ref, rt.Response{Decision: "allow_once"})
	s.Error(responseErr)
	s.Equal(rt.Known(false), receipt.HandedToSDK)
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
			for {
				event, err := session.Next(ctx)
				s.Require().NoError(err)
				if event.Diagnostic.NativeKind == tc.nativeKind || event.Origin.SourceKind == tc.nativeKind {
					s.Equal(tc.kind, event.Kind)
					if event.Kind == rt.Unknown {
						s.Len(event.Diagnostic.SHA256, 64)
					}
					s.NotContains(testutil.MessageText(event), "synthetic-sensitive-marker")
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
	event := s.nextInteraction(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	ref := event.Interaction.Ref
	_, cancelErr := session.Cancel(ctx, session.Snapshot().Foreground.OperationRef)
	s.Require().NoError(cancelErr)
	receipt, responseErr := session.Respond(ctx, ref, rt.Response{Decision: "allow_once"})
	s.Error(responseErr)
	s.Equal(rt.Known(false), receipt.HandedToSDK)
}
