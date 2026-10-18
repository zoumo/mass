package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type lifecycleSuite struct {
	suite.Suite
	executables map[string]string
}

func TestLifecycle(t *testing.T) { suite.Run(t, new(lifecycleSuite)) }
func (s *lifecycleSuite) SetupSuite() {
	s.executables = map[string]string{}
	for _, kind := range []string{"codex", "claude"} {
		executable := filepath.Join(s.T().TempDir(), "mock-"+kind)
		output, err := exec.Command("go", "build", "-o", executable, "github.com/zoumo/agent-sdk-go/"+kind+"/cmd/mock").CombinedOutput()
		s.Require().NoError(err, string(output))
		s.executables[kind] = executable
	}
}
func (s *lifecycleSuite) scenario(kind string, frames []string) string {
	s.T().Helper()
	records := []map[string]any{}
	for i, frame := range frames {
		delay := 0
		if strings.HasPrefix(frame, "delay=") {
			prefix, rest, _ := strings.Cut(frame, " ")
			_, err := fmt.Sscanf(prefix, "delay=%d", &delay)
			s.Require().NoError(err)
			frame = rest
		}
		direction, rest, _ := strings.Cut(frame, " ")
		channel := "protocol"
		key := "wire"
		if kind == "claude" {
			channel, rest, _ = strings.Cut(rest, " ")
			key = "data"
		}
		record := map[string]any{"ordinal": i + 1, "direction": direction, "channel": channel, key: json.RawMessage(rest)}
		if kind == "claude" {
			record["delay_ms"] = delay
		}
		if kind == "codex" {
			record["delayMs"] = delay
			record["epoch"] = 1
		}
		records = append(records, record)
	}
	data, err := json.Marshal(map[string]any{"version": 1, "provenance": "synthetic", "frames": records})
	s.Require().NoError(err)
	path := filepath.Join(s.T().TempDir(), "scenario.json")
	s.Require().NoError(os.WriteFile(path, data, 0600))
	return path
}
func codexInit() []string {
	return []string{
		`c2s {"id":1,"method":"initialize","params":{"clientInfo":{"name":"mass-go","version":"experiment"}}}`,
		`s2c {"id":1,"result":{"userAgent":"synthetic","codexHome":"/synthetic","platformFamily":"unix","platformOs":"linux"}}`,
		`c2s {"method":"initialized"}`,
	}
}
func codexThread(id int, session string) []string {
	return []string{
		fmt.Sprintf(`c2s {"id":%d,"method":"thread/start","params":{"ephemeral":true}}`, id),
		fmt.Sprintf(`s2c {"id":%d,"result":{"thread":{"id":%q,"sessionId":"different-tree-group","cliVersion":"synthetic","createdAt":1,"updatedAt":1,"cwd":"/synthetic","ephemeral":true,"modelProvider":"mock","preview":"","projectId":null,"source":"appServer","status":{"type":"idle"},"turns":[]},"approvalPolicy":"never","approvalsReviewer":"user","cwd":"/synthetic","model":"fake","modelProvider":"mock","sandbox":{"type":"readOnly"}}}`, id, session),
	}
}
func codexTurn(id int, session string) []string {
	return []string{
		fmt.Sprintf(`c2s {"id":%d,"method":"turn/start","params":{"threadId":%q,"input":[{"type":"text","text":"hello","text_elements":[]}]}}`, id, session),
		fmt.Sprintf(`s2c {"id":%d,"result":{"turn":{"id":"different-work-id","status":"inProgress","items":[],"error":null}}}`, id),
		fmt.Sprintf(`s2c {"method":"item/agentMessage/delta","params":{"threadId":%q,"turnId":"different-work-id","itemId":"different-message-id","delta":%q}}`, session, session+" reply"),
	}
}
func claudeFrames(session string, second bool) []string {
	frames := []string{
		`client control {"type":"control_request","request_id":"init","request":{"subtype":"initialize","hooks":null,"forwardSubagentText":true}}`,
		`server control {"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}`,
		`client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`,
		fmt.Sprintf(`server protocol {"type":"system","subtype":"init","session_id":%q}`, session),
		fmt.Sprintf(`server protocol {"type":"assistant","uuid":"different-envelope-id","session_id":%q,"message":{"id":"different-message-id","role":"assistant","content":[{"type":"text","text":%q}]}}`, session, session+" reply"),
	}
	if second {
		frames = append(frames,
			`client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`,
			fmt.Sprintf(`server protocol {"type":"assistant","session_id":%q,"message":{"id":"message-2","role":"assistant","content":[{"type":"text","text":%q}]}}`, session, session+" reply"))
	}
	return frames
}
func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func (s *lifecycleSuite) config(kind string) (rt.Config, []string) {
	dir := s.T().TempDir()
	wrapper := filepath.Join(dir, "mock")
	pids := []string{filepath.Join(dir, "first.pid")}
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" >> " + quote(wrapper+".launches") + "\n"
	if kind == "codex" {
		frames := append(codexInit(), codexThread(2, "native-A")...)
		frames = append(frames, codexThread(3, "native-B")...)
		frames = append(frames, codexTurn(4, "native-A")...)
		frames = append(frames, codexTurn(5, "native-B")...)
		frames = append(frames, codexTurn(6, "native-B")...)
		path := s.scenario(kind, frames)
		script += "export CODEX_MOCK_SCENARIO=" + quote(path) + "\nprintf '%s' \"$$\" > " + quote(pids[0]) + "\n"
	} else {
		pids = append(pids, filepath.Join(dir, "second.pid"))
		a, b := s.scenario(kind, claudeFrames("native-A", false)), s.scenario(kind, claudeFrames("native-B", true))
		script += "if mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nexport CLAUDE_MOCK_SCENARIO=" + quote(a) + "\nprintf '%s' \"$$\" > " + quote(pids[0]) + "\nelse\nexport CLAUDE_MOCK_SCENARIO=" + quote(b) + "\nprintf '%s' \"$$\" > " + quote(pids[1]) + "\nfi\n"
	}
	script += "exec " + quote(s.executables[kind]) + " \"$@\"\n"
	s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
	return rt.Config{Executable: wrapper, Env: []string{}}, pids
}
func (s *lifecycleSuite) readReply(ctx context.Context, session rt.Session) {
	for {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Message != nil {
			s.Equal(session.Key().RuntimeName, event.RuntimeName)
			s.Equal(session.Key().SessionID, event.SessionID)
			s.Equal(session.Key().SessionID+" reply", testutil.MessageText(event))
			return
		}
	}
}
func (s *lifecycleSuite) assertExited(path string) {
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	var pid int
	_, err = fmt.Sscan(string(data), &pid)
	s.Require().NoError(err)
	s.Eventually(func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, time.Second, 10*time.Millisecond)
}
func (s *lifecycleSuite) TestNamedRuntimeIsolation() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := catalog.New()
			runtimes := []rt.Runtime{}
			configs := []rt.Config{}
			pidFiles := [][]string{}
			for _, name := range []string{"first", "second"} {
				config, pids := s.config(kind)
				configs = append(configs, config)
				pidFiles = append(pidFiles, pids)
				_, err := c.Register(rt.Registration{Name: name, Type: kind, Config: config})
				s.Require().NoError(err)
				runtime, err := c.Get(name)
				s.Require().NoError(err)
				runtimes = append(runtimes, runtime)
				s.T().Cleanup(func() { s.NoError(runtime.Stop()) })
				for _, pid := range pids {
					_, err := os.Stat(pid)
					s.True(os.IsNotExist(err), "registration must not launch a process")
				}
			}
			var group sync.WaitGroup
			errs := make(chan error, 16)
			for i := 0; i < 16; i++ {
				group.Add(1)
				go func() { defer group.Done(); errs <- runtimes[i%2].Start(ctx) }()
			}
			group.Wait()
			close(errs)
			for err := range errs {
				s.Require().NoError(err)
			}
			keys := []rt.SessionKey{}
			attachments := [][]rt.Session{}
			for i, runtime := range runtimes {
				snapshot := runtime.Status()
				s.Equal("ready", snapshot.Phase)
				s.Equal(snapshot.ConfigRevision, snapshot.ObservedRevision)
				if kind == "claude" {
					s.Equal("unknown", snapshot.Health.Connected.State)
					for _, pid := range pidFiles[i] {
						_, err := os.Stat(pid)
						s.True(os.IsNotExist(err))
					}
				} else {
					s.Equal("known", snapshot.Health.ProtocolResponsive.State)
				}
				_, err := c.UpdateConfig(snapshot.Name, configs[i], snapshot.ConfigRevision)
				s.ErrorIs(err, rt.ErrConflict)
				s.ErrorIs(c.Unregister(snapshot.Name), rt.ErrConflict)
				options := rt.SessionOptions{}
				input := "hello"
				if kind == "claude" {
					options.InitialInput = &input
				}
				a := testutil.OpenSession(s.T(), runtime, ctx, options)
				b := testutil.OpenSession(s.T(), runtime, ctx, options)
				for _, session := range []rt.Session{a, b} {
					if kind == "codex" {
						receipt, err := session.Submit(ctx, "hello")
						s.Require().NoError(err)
						s.Equal(rt.NativeAcknowledged, receipt.Evidence)
					}
					s.readReply(ctx, session)
				}
				s.Equal(rt.SessionKey{RuntimeName: snapshot.Name, SessionID: "native-A"}, a.Key())
				s.Equal("native-B", b.Key().SessionID)
				s.Len(runtime.ListSessions(), 2)
				got, err := runtime.GetSession(a.Key())
				s.Require().NoError(err)
				s.Equal(a, got)
				keys = append(keys, a.Key())
				attachments = append(attachments, []rt.Session{a, b})
			}
			s.Equal(keys[0].SessionID, keys[1].SessionID)
			s.NotEqual(keys[0].RuntimeName, keys[1].RuntimeName)
			_, err := runtimes[0].GetSession(keys[1])
			s.ErrorIs(err, rt.ErrNotFound)
			for i, runtime := range runtimes {
				snapshot := runtime.Status()
				a, b := attachments[i][0], attachments[i][1]
				s.NoError(a.Close())
				s.Equal("closed", a.Snapshot().Attachment)
				if kind == "claude" {
					s.assertExited(pidFiles[i][0])
				}
				_, err := a.Submit(ctx, "hello")
				s.Error(err)
				_, err = b.Submit(ctx, "hello")
				s.Require().NoError(err)
				s.readReply(ctx, b)
				s.NoError(runtime.Stop())
				for _, pid := range pidFiles[i] {
					s.assertExited(pid)
				}
				launches, err := os.ReadFile(configs[i].Executable + ".launches")
				s.Require().NoError(err)
				expectedProcesses := 1
				if kind == "claude" {
					expectedProcesses = 2
				}
				s.Len(strings.Fields(string(launches)), expectedProcesses, "activation must not create duplicate processes")
				s.Len(runtime.ListSessions(), 2)
				_, err = runtime.GetSession(a.Key())
				s.NoError(err)
				_, err = c.UpdateConfig(snapshot.Name, configs[i], snapshot.ConfigRevision)
				s.ErrorIs(err, rt.ErrConflict)
				s.ErrorIs(c.Unregister(snapshot.Name), rt.ErrConflict)
			}
		})
	}
}

func (s *lifecycleSuite) TestStoppedRuntimeCanActivateAgain() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			config, pids := s.config(kind)
			c := catalog.New()
			_, err := c.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			runtime, err := c.Get("worker")
			s.Require().NoError(err)
			s.T().Cleanup(func() { s.NoError(runtime.Stop()) })
			for range 2 {
				s.Require().NoError(runtime.Start(ctx))
				s.Equal("ready", runtime.Status().Phase)
				s.Require().NoError(runtime.Stop())
				s.Equal("stopped", runtime.Status().Phase)
				if kind == "codex" {
					s.assertExited(pids[0])
				}
			}
			s.NoError(c.Unregister("worker"))
			s.ErrorIs(runtime.Start(ctx), rt.ErrConflict)
		})
	}
}
