package vertical_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

var mock, claudeMock, app, ctl, root string

// Blocks the identity commit while the real adapter already emitted initial idle.
type identityGate struct {
	store.MetaStore
	entered chan struct{}
	release chan struct{}
}

func (g *identityGate) CreateSession(r resource.Session) (resource.Session, error) {
	close(g.entered)
	<-g.release
	return g.MetaStore.CreateSession(r)
}

func TestMain(m *testing.M) {
	var err error
	root, err = os.MkdirTemp("", "mass-vertical-")
	if err != nil {
		panic(err)
	}
	code := 1
	func() {
		defer func() {
			output, err := exec.Command("ps", "-axo", "pid=,args=").Output()
			if err != nil {
				fmt.Fprintln(os.Stderr, "FAIL process readback", err)
				code = 1
			}
			leaked := false
			for _, line := range strings.Split(string(output), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && strings.HasPrefix(fields[1], root+"/") {
					leaked = true
					code = 1
					pid, err := strconv.Atoi(fields[0])
					if err == nil {
						_ = syscall.Kill(pid, syscall.SIGTERM)
					}
				}
			}
			if leaked {
				fmt.Fprintln(os.Stderr, "FAIL: leaked owned child; cleanup SIGTERM sent")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				output, err = exec.Command("ps", "-axo", "pid=,args=").Output()
				if err != nil {
					code = 1
					break
				}
				if !bytes.Contains(output, []byte(root+"/")) {
					fmt.Println("PASS no owned SDK peer/application processes remain")
					break
				}
				if time.Now().After(deadline) {
					fmt.Fprintln(os.Stderr, "FAIL owned process cleanup readback")
					code = 1
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if os.RemoveAll(root) != nil {
				code = 1
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				code = 1
			}
		}()
		for name, target := range map[string]string{"claude-mock": "github.com/zoumo/agent-sdk-go/claude/cmd/mock", "mock": "github.com/zoumo/agent-sdk-go/codex/cmd/mock", "massd": "../../cmd/massd", "massctl": "../../cmd/massctl"} {
			bin := filepath.Join(root, name)
			cmd := exec.Command("go", "build", "-o", bin, target)
			if out, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "build %s: %v\n%s", name, err, out)
				return
			}
			switch name {
			case "claude-mock":
				claudeMock = bin
			case "mock":
				mock = bin
			case "massd":
				app = bin
			case "massctl":
				ctl = bin
			}
		}
		code = m.Run()
	}()
	os.Exit(code)
}
func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}
func scenario(t *testing.T) (string, string) {
	t.Helper()
	work := t.TempDir()
	data, err := os.ReadFile("testdata/codex.json")
	require.NoError(t, err)
	quoted, err := json.Marshal(work)
	require.NoError(t, err)
	data = bytes.ReplaceAll(data, []byte(`"/synthetic"`), quoted)
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path, work
}
func TestCodexVertical(t *testing.T) {
	c := ctx(t)
	path, work := scenario(t)
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	registry := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{}})
	gate := &identityGate{MetaStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	defer release()
	svc := service.New(gate, st, registry)
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))) })
	r, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Metadata.Generation)
	_, err = registry.Get("codex")
	require.Error(t, err, "resource create must not construct a Driver")
	r, err = svc.StartRuntime(c, "codex")
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Status.AppliedGeneration)
	driver, err := registry.Get("codex")
	require.NoError(t, err)
	_, err = driver.Events()
	require.Error(t, err, "Service has already acquired the only stream")
	type createdResult struct {
		value resource.Session
		err   error
	}
	created := make(chan createdResult, 1)
	go func() {
		value, createErr := svc.CreateSession(c, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "codex", SessionSpec: runtime.SessionSpec{Cwd: work}}})
		created <- createdResult{value: value, err: createErr}
	}()
	select {
	case <-gate.entered:
	case <-c.Done():
		t.Fatal(c.Err())
	}
	_, err = svc.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.ListHistory("codex-session", store.HistoryQuery{})
	require.ErrorIs(t, err, store.ErrNotFound)
	release()
	var opened createdResult
	select {
	case opened = <-created:
	case <-c.Done():
		t.Fatal(c.Err())
	}
	session, err := opened.value, opened.err
	require.NoError(t, err)
	require.Equal(t, "codex-session", session.Status.SessionID)
	id := resource.SessionRef{SessionID: session.Status.SessionID}
	name := resource.SessionRef{Namespace: "default", Name: "demo"}
	byID, err := svc.GetSession(id)
	require.NoError(t, err)
	byName, err := svc.GetSession(name)
	require.NoError(t, err)
	require.Equal(t, byID, byName)
	before, err := svc.ListHistoryEvents(id, store.HistoryQuery{})
	require.NoError(t, err)
	require.Empty(t, before.Events)
	require.Nil(t, before.SyncedEventID)
	t.Log("PASS no-input native identity; strict peer is waiting for separate turn/start")
	sub, err := svc.SubscribeEvents(name)
	require.NoError(t, err)
	defer sub.Close()
	observer, err := svc.SubscribeEvents(id)
	require.NoError(t, err)
	require.NoError(t, observer.Close())
	receipt, err := svc.SendPrompt(c, id, runtime.Input{Content: []runtime.ContentBlock{runtime.Text("hello")}})
	require.NoError(t, err)
	require.Equal(t, "turn-1", receipt.Submission)
	require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
	persisted := []runtime.SessionEvent{}
	chunks := []string{}
	running := false
	for {
		event, err := sub.Next(c)
		require.NoError(t, err)
		require.Equal(t, "default", event.Namespace)
		require.Equal(t, "demo", event.SessionName)
		if event.Type() != "session.state_update" {
			require.Equal(t, "turn-1", event.Source().Work())
		}
		wire, err := json.Marshal(event)
		require.NoError(t, err)
		for _, forbidden := range []string{`"seq"`, `"schemaVersion"`, `"run"`, `"work"`, `"turn-1"`} {
			require.NotContains(t, string(wire), forbidden)
		}
		if event.Persistent() {
			if event.Type() != "agent.tool_call" {
				require.Equal(t, time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC), event.Time)
			} // Read through a newly opened file Store immediately on publication.
			reopened, err := file.Open(dir, true)
			require.NoError(t, err)
			page, err := reopened.ListHistory(event.SessionID, store.HistoryQuery{})
			require.NoError(t, err)
			require.NoError(t, reopened.Close())
			found := false
			for _, saved := range page.Events {
				if saved.EventID == event.EventID {
					b, err := json.Marshal(saved)
					require.NoError(t, err)
					require.JSONEq(t, string(wire), string(b))
					found = true
				}
			}
			require.True(t, found, "publication must follow durable readable file commit")
			persisted = append(persisted, event)
		}
		if chunk, ok := event.Payload().(runtime.MessageChunk); ok {
			require.Equal(t, "message-1", event.ItemID)
			chunks = append(chunks, chunk.Content[0].Text)
		}
		if state, ok := event.Payload().(runtime.StateUpdate); ok && state.State != nil {
			if *state.State == runtime.Running {
				running = true
			}
			if running && *state.State == runtime.Idle {
				break
			}
		}
	}
	require.Equal(t, []string{"preview ", "only"}, chunks)
	require.Len(t, persisted, 4)
	require.Equal(t, "complete answer", persisted[3].Payload().(runtime.AgentMessage).Content[0].Text)
	page, err := svc.ListHistoryEvents(id, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 4)
	for _, event := range page.Events {
		require.True(t, event.Persistent())
	}
	progress, err := st.Append(session.Status.SessionID, persisted)
	require.NoError(t, err)
	require.Equal(t, page.SyncedEventID, progress)
	page2, err := st.ListHistory(session.Status.SessionID, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page2.Events, 4)
	require.NoError(t, sub.Close())
	t.Log("PASS separate input, typed events, delta-only previews, commit-before-publish, UUID retry")
	require.NoError(t, svc.Shutdown(c))
	require.NoError(t, st.Close())
	reopened, err := file.Open(dir, true)
	require.NoError(t, err)
	defer reopened.Close()
	offline := service.New(reopened, reopened, nil)
	savedID, err := offline.GetSession(id)
	require.NoError(t, err)
	savedName, err := offline.GetSession(name)
	require.NoError(t, err)
	require.Equal(t, savedID, savedName)
	require.Equal(t, session.Metadata.UID, savedID.Metadata.UID)
	history, err := offline.ListHistoryEvents(id, store.HistoryQuery{})
	require.NoError(t, err)
	require.Equal(t, page2, history)
	_, err = offline.SendPrompt(c, id, runtime.Input{Content: []runtime.ContentBlock{runtime.Text("no")}})
	require.Error(t, err)
	t.Log("PASS reopened read-only identity/history with no Registry or Driver")
}

func TestApplicationEntrypoints(t *testing.T) {
	path, work := scenario(t)
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx(t), app, "-data", dir, "-codex", mock, "-scenario", path, "-cwd", work)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), `"type":"agent.message"`)
	require.Contains(t, string(out), "complete answer")
	cmd = exec.CommandContext(ctx(t), ctl, "-data", dir, "-id", "codex-session", "-history")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var page store.HistoryPage
	require.NoError(t, json.Unmarshal(out, &page))
	require.Len(t, page.Events, 4)
	cmd = exec.CommandContext(ctx(t), ctl, "-data", dir, "-namespace", "default", "-name", "demo")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var identity resource.Session
	require.NoError(t, json.Unmarshal(out, &identity))
	require.Equal(t, "codex-session", identity.Status.SessionID)
	t.Log("PASS built massd workflow and offline massctl readback")
}
