package vertical_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type entrypointSuite struct {
	suite.Suite
	url, data string
	daemon    *exec.Cmd
	stderr    bytes.Buffer
}

func TestEntrypointSuite(t *testing.T) { suite.Run(t, new(entrypointSuite)) }
func (s *entrypointSuite) SetupTest() {
	daemonCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	s.T().Cleanup(cancel)
	s.data = s.T().TempDir()
	s.stderr.Reset()
	s.daemon = exec.CommandContext(daemonCtx, app, "--data", s.data, "--listen", "127.0.0.1:0", "--local", "--prototype-auto-approve-permissions")
	s.daemon.Stderr = &s.stderr
	out, e := s.daemon.StdoutPipe()
	s.Require().NoError(e)
	s.Require().NoError(s.daemon.Start())
	var ready struct {
		URL string `json:"url"`
	}
	s.Require().NoError(json.NewDecoder(out).Decode(&ready))
	s.url = ready.URL
	s.T().Cleanup(func() {
		s.Require().NoError(s.daemon.Process.Signal(syscall.SIGTERM))
		done := make(chan error, 1)
		go func() { done <- s.daemon.Wait() }()
		select {
		case e := <-done:
			s.Require().NoError(e, s.stderr.String())
		case <-time.After(15 * time.Second):
			_ = s.daemon.Process.Kill()
			<-done
			s.Fail("daemon failed graceful release", s.stderr.String())
		}
		conn, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(daemonCtx, "tcp", strings.TrimPrefix(s.url, "http://"))
		if conn != nil {
			conn.Close()
		}
		s.Require().Error(e, "owned listener must be released")
		s.Require().NoError(os.RemoveAll(s.data))
		_, e = os.Stat(s.data)
		s.True(os.IsNotExist(e))
		s.T().Log("PASS synthetic daemon exit, owned port and data directory cleanup readback")
	})
}

func (s *entrypointSuite) call(op string, p client.Params, out any) {
	s.T().Helper()
	b, e := client.EncodeParams(op, p)
	s.Require().NoError(e)
	cmd := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "--op", op)
	cmd.Stdin = bytes.NewReader(b)
	v, e := cmd.CombinedOutput()
	s.Require().NoError(e, "%s: %s", op, v)
	if out != nil {
		s.Require().NoError(json.Unmarshal(v, out))
	}
}

func (s *entrypointSuite) open(kind, mode string) (resource.SessionRef, resource.Session, string) {
	path, work := nativeScenario(s.T(), kind, mode)
	bin := mock
	if kind == "claude" {
		bin = claudeMock
	}
	r := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}}
	s.call("runtime.create", client.Params{Runtime: &r}, nil)
	s.call("runtime.start", client.Params{Name: "agent"}, nil)
	var session resource.Session
	create := resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: "session"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}}
	s.call("session.create", client.Params{Create: &create}, &session)
	s.NotEmpty(session.Status.SessionID)
	ref := resource.SessionRef{SessionID: session.Status.SessionID}
	s.Eventually(func() bool {
		var v resource.Session
		s.call("session.get", client.Params{Session: ref}, &v)
		return v.Status.State == events.Idle || (kind == "claude" && v.Status.State == events.Unknown)
	}, 2*time.Second, 10*time.Millisecond)
	var page store.HistoryPage
	s.call("session.history", client.Params{Session: ref}, &page)
	for _, e := range page.Events {
		s.NotEqual("user.message", e.Type(), "creation must not submit input")
	}
	return ref, session, work
}

func (s *entrypointSuite) observer(ref resource.SessionRef) (*exec.Cmd, *bufio.Scanner) {
	b, e := json.Marshal(ref)
	s.Require().NoError(e)
	cmd := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "--op", "observe")
	cmd.Stdin = bytes.NewReader(b)
	out, e := cmd.StdoutPipe()
	s.Require().NoError(e)
	cmd.Stderr = os.Stderr
	s.Require().NoError(cmd.Start())
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 4096), 16<<20)
	s.Require().True(scan.Scan())
	s.JSONEq(`{"ready":true}`, scan.Text())
	s.T().Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	return cmd, scan
}

func (s *entrypointSuite) history(ref resource.SessionRef) store.HistoryPage {
	var all store.HistoryPage
	after := ""
	for {
		var page store.HistoryPage
		s.call("session.history", client.Params{Session: ref, History: store.HistoryQuery{Limit: 2, After: after}}, &page)
		all.Events = append(all.Events, page.Events...)
		all.SyncedEventID = page.SyncedEventID
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	return all
}

func (s *entrypointSuite) runWork(ref resource.SessionRef, text string, question bool) []events.SessionEvent {
	_, scan := s.observer(ref)
	var receipt runtime.InputReceipt
	s.call("session.input", client.Params{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, &receipt)
	s.NotEmpty(receipt.Submission)
	seen := []events.SessionEvent{}
	questions := 0
	chunks := 0
	permissions := 0
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		seen = append(seen, e)
		if e.Persistent() {
			page := s.history(ref)
			found := false
			for _, stored := range page.Events {
				if stored.EventID == e.EventID {
					s.Equal(stored, e, "published fact must equal committed query value")
					found = true
				}
			}
			s.True(found, "publish after persist")
		}
		switch p := e.Payload().(type) {
		case events.InteractionRequested:
			if p.Request.Kind == "form" {
				questions++
				s.Require().True(question, "questions are never permission-approved")
				answer := map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"format": map[string]any{"answers": []any{"json"}}}}}
				if strings.HasPrefix(ref.SessionID, "claude") {
					answer = map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"format": "json"}}}
				}
				s.call("session.answer", client.Params{Session: ref, Interaction: p.Interaction, Answer: answer}, nil)
			} else {
				permissions++
			}
		case events.MessageChunk:
			chunks++
		case events.StateUpdate:
			if p.State != nil && *p.State == events.Idle {
				if question {
					s.Equal(1, questions)
					s.T().Log("PASS synthetic explicit user question via CLI/HTTP and original SDK handle")
				} else {
					if text != "continue" || strings.HasPrefix(ref.SessionID, "claude") {
						s.Positive(permissions)
					}
					if permissions > 0 {
						s.T().Log("PASS synthetic automatic permission replies via original SDK handles")
					}
				}
				s.Positive(chunks)
				return seen
			}
		}
	}
	s.Require().NoError(scan.Err())
	s.FailNow("observer ended before work completed")
	return nil
}

func (s *entrypointSuite) basic(kind string) {
	ref, original, work := s.open(kind, "basic")
	// Closing a registered observer does not archive or cancel the native session.
	watch, e := (client.Client{URL: s.url}).Observe(ctx(s.T()), ref)
	s.Require().NoError(e)
	s.Require().NoError(watch.Close())
	s.runWork(ref, "hello", false)
	if kind == "claude" {
		var out any
		e = (client.Client{URL: s.url}).Call(ctx(s.T()), "session.input", client.Params{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("forbidden second input")}}}, &out)
		s.Require().Error(e)
		s.T().Log("PASS Claude second input rejected; close and original-ID resume required")
	}
	for _, q := range []store.HistoryQuery{{Types: []string{}}, {Limit: -1}, {After: "not-a-cursor"}} {
		e = (client.Client{URL: s.url}).Call(ctx(s.T()), "session.history", client.Params{Session: ref, History: q}, nil)
		var public *transport.Error
		s.Require().ErrorAs(e, &public)
		s.Equal("invalid_argument", public.Code)
	}
	before := s.history(ref)
	s.NotEmpty(before.Events)
	for _, e := range before.Events {
		s.True(e.Persistent())
		s.NotContains(e.Type(), "chunk")
	}
	var filtered store.HistoryPage
	s.call("session.history", client.Params{Session: ref, History: store.HistoryQuery{Types: []string{"agent.message"}, Limit: 1}}, &filtered)
	s.Require().Len(filtered.Events, 1)
	s.Equal("agent.message", filtered.Events[0].Type())
	s.call("session.archive", client.Params{Session: ref}, nil)
	resumePath, _ := nativeScenario(s.T(), kind, "lifecycle-resume")
	resumePath = replaceWorkspace(s.T(), resumePath, work)
	bin := mock
	if kind == "claude" {
		bin = claudeMock
	}
	spec := resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": resumePath}}
	s.call("runtime.update", client.Params{Name: "agent", Spec: &spec}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	// Durable full history readback uses the file Store, with no driver construction.
	st, e := file.Open(s.data, true)
	s.Require().NoError(e)
	disk, e := st.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(e)
	s.Equal(before, disk)
	s.Require().NoError(st.Close())
	s.call("runtime.start", client.Params{Name: "agent"}, nil)
	var resumed resource.Session
	s.call("session.resume", client.Params{Session: ref}, &resumed)
	s.Equal(original.Metadata.UID, resumed.Metadata.UID)
	s.Equal(original.Status.SessionID, resumed.Status.SessionID)
	s.Equal(before, s.history(ref))
	s.runWork(ref, "continue", false)
	after := s.history(ref)
	s.Greater(len(after.Events), len(before.Events))
	s.Equal(before.Events, after.Events[:len(before.Events)])
	s.call("session.archive", client.Params{Session: ref}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	var byName resource.Session
	s.call("session.get", client.Params{Session: resource.SessionRef{Namespace: "demo", Name: "session"}}, &byName)
	s.Equal(ref.SessionID, byName.Status.SessionID)
	var sessions resource.SessionPage
	s.call("session.list", client.Params{SessionQuery: resource.SessionQuery{Namespace: "demo", State: "archived", Limit: 1}}, &sessions)
	s.Len(sessions.Items, 1)
	s.T().Log("PASS synthetic no-input identity, independent input, live chunks, persisted complete facts, filtering/pagination, unsubscribe isolation, archive/stop/same-ID resume and fresh input via product CLI/HTTP+SSE")
}
func (s *entrypointSuite) TestCodexBasicRecovery()  { s.basic("codex") }
func (s *entrypointSuite) TestClaudeBasicRecovery() { s.basic("claude") }
func (s *entrypointSuite) question(kind string) {
	ref, _, _ := s.open(kind, "question")
	s.runWork(ref, "hello", true)
	s.call("session.archive", client.Params{Session: ref}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
}
func (s *entrypointSuite) TestCodexQuestion()  { s.question("codex") }
func (s *entrypointSuite) TestClaudeQuestion() { s.question("claude") }
func (s *entrypointSuite) cancel(kind string) {
	ref, _, _ := s.open(kind, "cancel")
	_, scan := s.observer(ref)
	s.call("session.input", client.Params{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}}, nil)
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Running {
			break
		}
	}
	// Disconnect while work remains executing, then cancel through another client.
	o, e := (client.Client{URL: s.url}).Observe(ctx(s.T()), ref)
	s.Require().NoError(e)
	s.Require().NoError(o.Close())
	var state resource.Session
	s.call("session.get", client.Params{Session: ref}, &state)
	s.Equal(events.Running, state.Status.State)
	var ack runtime.CancelReceipt
	s.call("session.cancel", client.Params{Session: ref}, &ack)
	s.Equal("controlAcknowledged", ack.Result)
	s.NotEmpty(ack.Target)
	s.Eventually(func() bool {
		s.call("session.get", client.Params{Session: ref}, &state)
		return state.Status.State == events.Idle
	}, 3*time.Second, 10*time.Millisecond)
	s.call("session.archive", client.Params{Session: ref}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	s.T().Log("PASS synthetic disconnect during running work does not cancel; explicit cancellation acknowledged and later termination confirmed before closure")
}
func (s *entrypointSuite) TestCodexCancellation()  { s.cancel("codex") }
func (s *entrypointSuite) TestClaudeCancellation() { s.cancel("claude") }
func (s *entrypointSuite) TestParametersAndDirectory() {
	c := client.Client{URL: s.url}
	var out any
	for _, op := range []string{"unknown", "session.get", "runtime.get"} {
		e := c.Call(ctx(s.T()), op, client.Params{}, &out)
		var public *transport.Error
		s.Require().ErrorAs(e, &public)
		s.Equal("invalid_argument", public.Code)
	}
	r := resource.Runtime{Metadata: resource.Metadata{Name: "unused"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": filepath.Join(s.T().TempDir(), "absent")}}}
	s.call("runtime.create", client.Params{Runtime: &r}, nil)
	var rs []resource.Runtime
	s.call("runtime.list", client.Params{}, &rs)
	s.Len(rs, 1)
	s.call("runtime.delete", client.Params{Name: "unused"}, nil)
	e := c.Call(ctx(s.T()), "runtime.get", client.Params{Name: "unused"}, nil)
	var public *transport.Error
	s.Require().ErrorAs(e, &public)
	s.Equal("not_found", public.Code)
	cmd := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "--op", "session.get")
	cmd.Stdin = strings.NewReader(`{"unexpected":"secret"}`)
	v, e := cmd.CombinedOutput()
	s.Require().Error(e)
	s.NotContains(string(v), "secret")
	for _, body := range []string{`null`, `{} {}`, `{"metadata":{}}`, `{"unexpected":"private-diagnostic"}`} {
		request, e := http.NewRequestWithContext(ctx(s.T()), http.MethodPost, s.url+"/v1/sessions", strings.NewReader(body))
		s.Require().NoError(e)
		request.Header.Set("Content-Type", "application/json")
		response, e := http.DefaultClient.Do(request)
		s.Require().NoError(e)
		var v transport.Error
		s.Require().NoError(json.NewDecoder(response.Body).Decode(&v))
		response.Body.Close()
		s.Equal("invalid_argument", v.Code)
		s.NotContains(v.Message, "private-diagnostic")
	}

	s.T().Log("PASS invalid parameters, unknown operation, runtime list/delete, stable redacted not_found and CLI parse errors")
}

func (s *entrypointSuite) TestCodexQuestionBeforeACK() {
	ref, _, _ := s.open("codex", "question-before-ack")
	s.runWork(ref, "hello", true)
	s.call("session.archive", client.Params{Session: ref}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	s.T().Log("PASS synthetic native request preceding turn ACK is bounded and retained with original handle")
}

func (s *entrypointSuite) TestFailedCreateCleanup() {
	path, work := nativeScenario(s.T(), "claude", "missing-disconnect")
	r := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}}
	s.call("runtime.create", client.Params{Runtime: &r}, nil)
	s.call("runtime.start", client.Params{Name: "agent"}, nil)
	create := resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: "session"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}}
	c := client.Client{URL: s.url}
	e := c.Call(ctx(s.T()), "session.create", client.Params{Create: &create}, nil)
	s.Require().Error(e)
	var public *transport.Error
	s.Require().ErrorAs(e, &public)
	s.NotContains(public.Message, work)
	s.NotContains(public.Message, "EOF")
	var page resource.SessionPage
	s.call("session.list", client.Params{}, &page)
	s.Empty(page.Items)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	s.call("runtime.delete", client.Params{Name: "agent"}, nil)
	ref, _, _ := s.open("claude", "basic")
	s.runWork(ref, "hello", false)
	s.call("session.archive", client.Params{Session: ref}, nil)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	s.T().Log("PASS synthetic missing native identity fails without publishing a Session; original resources settle, same name can be recreated and used")
}

func (s *entrypointSuite) TestDualAgentDirectory() {
	refs := map[string]resource.SessionRef{}
	for _, kind := range []string{"codex", "claude"} {
		path, work := nativeScenario(s.T(), kind, "basic")
		bin := mock
		if kind == "claude" {
			bin = claudeMock
		}
		r := resource.Runtime{Metadata: resource.Metadata{Name: kind}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}}
		s.call("runtime.create", client.Params{Runtime: &r}, nil)
		s.call("runtime.start", client.Params{Name: kind}, nil)
		create := resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: kind}, Spec: resource.SessionSpec{RuntimeRef: kind, SessionSpec: runtime.SessionSpec{Cwd: work}}}
		var session resource.Session
		s.call("session.create", client.Params{Create: &create}, &session)
		refs[kind] = resource.SessionRef{SessionID: session.Status.SessionID}
	}
	var first, second resource.SessionPage
	s.call("session.list", client.Params{SessionQuery: resource.SessionQuery{Namespace: "demo", Limit: 1}}, &first)
	s.Require().Len(first.Items, 1)
	s.Require().NotEmpty(first.Next)
	s.call("session.list", client.Params{SessionQuery: resource.SessionQuery{Namespace: "demo", After: first.Next, Limit: 2}}, &second)
	s.Require().Len(second.Items, 1)
	s.Empty(second.Next)
	s.NotEqual(first.Items[0].Status.SessionID, second.Items[0].Status.SessionID)
	e := (client.Client{URL: s.url}).Call(ctx(s.T()), "session.list", client.Params{SessionQuery: resource.SessionQuery{Namespace: "other", After: first.Next, Limit: 1}}, nil)
	var public *transport.Error
	s.Require().ErrorAs(e, &public)
	s.Equal("invalid_argument", public.Code)
	var rs []resource.Runtime
	s.call("runtime.list", client.Params{}, &rs)
	s.Len(rs, 2)
	for _, kind := range []string{"codex", "claude"} {
		s.runWork(refs[kind], "hello", false)
		s.call("session.archive", client.Params{Session: refs[kind]}, nil)
		s.call("runtime.stop", client.Params{Name: kind}, nil)
	}
	s.T().Log("PASS synthetic two SDK peers on one daemon; cross-Runtime Session pages preserve both IDs, exclusive next and filter binding; each input/live/history path stays independent")
}

func (s *entrypointSuite) unsupportedMedia(kind string) {
	ref, _, _ := s.open(kind, "basic")
	body, err := json.Marshal(map[string]any{"session": ref, "input": map[string]any{"content": []any{map[string]any{"type": "image", "data": "YQ==", "mimeType": "image/png"}}}})
	s.Require().NoError(err)
	err = (client.Client{URL: s.url}).CallJSON(ctx(s.T()), "session.input", body, nil)
	var public *transport.Error
	s.Require().ErrorAs(err, &public)
	s.Equal("unsupported", public.Code)
	// Unsupported content must fail before native submission, leaving the first text input usable.
	s.runWork(ref, "hello", false)
	var canceled runtime.CancelReceipt
	s.call("session.cancel", client.Params{Session: ref}, &canceled)
	s.Equal("noActiveWork", canceled.Result)
	s.Empty(canceled.Target)
	s.call("session.archive", client.Params{Session: ref}, nil)
	err = (client.Client{URL: s.url}).Call(ctx(s.T()), "session.get", client.Params{Session: ref}, nil)
	s.Require().NoError(err)
	_, err = (client.Client{URL: s.url}).Observe(ctx(s.T()), ref)
	s.Require().ErrorAs(err, &public)
	s.Equal("unavailable", public.Code)
	s.call("runtime.stop", client.Params{Name: "agent"}, nil)
	s.T().Log("PASS valid unsupported media rejected before native input; text still works; idle cancellation has no target; archived observation explicitly unavailable")
}
func (s *entrypointSuite) TestCodexUnsupportedMedia()  { s.unsupportedMedia("codex") }
func (s *entrypointSuite) TestClaudeUnsupportedMedia() { s.unsupportedMedia("claude") }
