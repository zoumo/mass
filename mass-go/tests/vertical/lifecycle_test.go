package vertical_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type runtimeStatusGate struct {
	store.MetaStore
	once             sync.Once
	entered, release chan struct{}
}

func (g *runtimeStatusGate) UpdateRuntime(r resource.Runtime) (resource.Runtime, error) {
	if r.Status.State == "running" {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return g.MetaStore.UpdateRuntime(r)
}
func TestConcurrentDesiredSpecAndStartStatus(t *testing.T) {
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	gate := &runtimeStatusGate{MetaStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	reg := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}})
	svc := service.New(gate, st, reg)
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))) })
	path, work := scenario(t)
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
	require.NoError(t, err)
	started := make(chan error, 1)
	go func() { _, e := svc.StartRuntime(ctx(t), "agent"); started <- e }()
	select {
	case <-gate.entered:
	case <-ctx(t).Done():
		t.Fatal("start did not reach status write")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := svc.UpdateRuntime("agent", resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": filepath.Join(dir, fmt.Sprintf("desired-%d.json", i))}})
			if e != nil {
				t.Errorf("update failed: %v", e)
			}
		}(i)
	}
	wg.Wait()
	desired, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.EqualValues(t, 17, desired.Metadata.Generation)
	release()
	require.NoError(t, <-started)
	applied, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, desired.Spec, applied.Spec)
	require.EqualValues(t, 17, applied.Metadata.Generation)
	require.EqualValues(t, 1, applied.Status.AppliedGeneration)
	r, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Status.RuntimeGeneration)
	finishInput(t, svc, resource.SessionRef{SessionID: r.Status.SessionID}, "hello")
	_, err = svc.StopRuntime(ctx(t), "agent")
	require.NoError(t, err)
	t.Log("PASS stale start-status CAS conflict preserves all 16 concurrent desired revisions; active Driver/session remain generation 1")
}

func lifecycleService(t *testing.T, dir string) (*service.Service, *file.Store, *runtime.Registry) {
	t.Helper()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	reg := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}})
	svc := service.NewWithPolicy(st, st, reg, prototype.AutoApprovePermissions)
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))); require.NoError(t, st.Close()) })
	return svc, st, reg
}
func finishInput(t *testing.T, svc *service.Service, ref resource.SessionRef, input string) {
	t.Helper()
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	defer sub.Close()
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []runtime.ContentBlock{runtime.Text(input)}})
	require.NoError(t, err)
	message := false
	for {
		e, err := sub.Next(ctx(t))
		require.NoError(t, err)
		if p, ok := e.Payload().(runtime.SessionError); ok {
			t.Fatalf("session failed: %s %s", p.Message, p.Reason)
		}
		if e.Type() == "agent.message" {
			message = true
		}
		if p, ok := e.Payload().(runtime.StateUpdate); ok && p.State != nil && *p.State == runtime.Idle && message {
			break
		}
	}
}

func TestCodexConfigurationStopAndColdResume(t *testing.T) {
	dir := t.TempDir()
	svc, st, reg := lifecycleService(t, dir)
	createPath, work := nativeScenario(t, "codex", "lifecycle-create")
	resumePath, _ := nativeScenario(t, "codex", "lifecycle-resume")
	// Both phases use the original workspace. Nothing is copied from personal data.
	resumePath = replaceWorkspace(t, resumePath, work)
	spec := resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": createPath}}
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "unused"}, Spec: spec})
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRuntime("unused"))
	_, err = svc.GetRuntime("unused")
	require.ErrorIs(t, err, store.ErrNotFound)
	r, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: spec})
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Metadata.Generation)
	r, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	driver, err := reg.Get("agent")
	require.NoError(t, err)
	first, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "z", Name: "same"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: first.Status.SessionID}
	finishInput(t, svc, ref, "hello")
	history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, history.Events, 4)
	// Desired type and config are mutable, even with live references.
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": resumePath}})
	require.NoError(t, err)
	spec.Config = runtime.Config{"executable": mock, "scenario": resumePath}
	updated, err := svc.UpdateRuntime("agent", spec)
	require.NoError(t, err)
	require.EqualValues(t, 3, updated.Metadata.Generation)
	require.EqualValues(t, 1, updated.Status.AppliedGeneration)
	again, err := svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	require.Equal(t, updated, again)
	sameDriver, err := reg.Get("agent")
	require.NoError(t, err)
	require.Same(t, driver, sameDriver)
	second, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "a", Name: "same"}, Spec: first.Spec})
	require.NoError(t, err)
	require.EqualValues(t, 1, second.Status.RuntimeGeneration, "new sessions still use old effective snapshot")
	require.Error(t, svc.DeleteRuntime("agent"))
	page, err := svc.ListSessions(resource.SessionQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, "a", page.Items[0].Metadata.Namespace)
	require.NotEmpty(t, page.Next)
	page2, err := svc.ListSessions(resource.SessionQuery{After: page.Next, Limit: 9})
	require.NoError(t, err)
	require.Equal(t, first.Status.SessionID, page2.Items[0].Status.SessionID)
	// Delay the native release notification. "closing" is observable before archived.
	archived := make(chan error, 1)
	go func() {
		_, e := svc.ArchiveSession(ctx(t), resource.SessionRef{Namespace: "z", Name: "same"})
		archived <- e
	}()
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.State == "closing" }, time.Second, time.Millisecond)
	select {
	case e := <-archived:
		t.Fatalf("archive preceded native release: %v", e)
	default:
	}
	require.NoError(t, <-archived)
	saved, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, runtime.SessionState("archived"), saved.Status.State)
	require.Zero(t, saved.Status.AppliedGeneration)
	_, err = svc.StopRuntime(ctx(t), "agent")
	require.NoError(t, err)
	_, err = reg.Get("agent")
	require.Error(t, err)
	offline, err := svc.GetSession(resource.SessionRef{SessionID: second.Status.SessionID})
	require.NoError(t, err)
	require.Zero(t, offline.Status.RuntimeGeneration)
	require.Equal(t, runtime.Unknown, offline.Status.State)
	stopped, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "stopped", stopped.Status.State)
	require.Zero(t, stopped.Status.AppliedGeneration)
	// Reading/listing never activates a Driver.
	_, err = svc.ListRuntimes(resource.RuntimeQuery{})
	require.NoError(t, err)
	_, err = svc.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	_, err = reg.Get("agent")
	require.Error(t, err)
	require.NoError(t, svc.Shutdown(ctx(t)))
	require.NoError(t, st.Close())

	cold, st2, reg2 := lifecycleService(t, dir)
	r, err = cold.GetRuntime("agent")
	require.NoError(t, err)
	// Simulate deletion/recreation by another resource owner. Service deletion
	// correctly forbids this while persistent references exist; UID is not ownership.
	require.NoError(t, st2.DeleteRuntime("agent", r.Metadata.ResourceVersion))
	recreated, err := cold.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: r.Spec})
	require.NoError(t, err)
	require.NotEqual(t, r.Metadata.UID, recreated.Metadata.UID)
	r, err = cold.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	require.EqualValues(t, 1, r.Status.AppliedGeneration)
	// Missing material must fail without consuming the native resume frame.
	files, err := filepath.Glob(filepath.Join(dir, "private", "codex", "*", "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, p := range files {
		require.NoError(t, os.Rename(p, p+".saved"))
	}
	_, err = cold.ResumeSession(ctx(t), ref)
	require.Error(t, err)
	for _, p := range files {
		require.NoError(t, os.Rename(p+".saved", p))
	}
	for _, p := range files {
		original, e := os.ReadFile(p)
		require.NoError(t, e)
		var m map[string]any
		require.NoError(t, json.Unmarshal(original, &m))
		if m["id"] != "codex-session" {
			continue
		}
		m["owner"] = "another-runtime"
		bad, e := json.Marshal(m)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(p, bad, 0600))
		_, e = cold.ResumeSession(ctx(t), ref)
		require.ErrorContains(t, e, "context mismatch")
		require.NoError(t, os.WriteFile(p, original, 0600))
	}
	resumed, err := cold.ResumeSession(ctx(t), resource.SessionRef{Namespace: "z", Name: "same"})
	require.NoError(t, err)
	require.Equal(t, first.Metadata.UID, resumed.Metadata.UID)
	require.Equal(t, first.Metadata.CreatedAt, resumed.Metadata.CreatedAt)
	require.Equal(t, first.Status.SessionID, resumed.Status.SessionID)
	require.Equal(t, first.Spec, resumed.Spec)
	_, err = cold.ResumeSession(ctx(t), ref)
	require.Error(t, err, "no competing controller")
	before, err := cold.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Equal(t, history, before, "resume metadata is not new history")
	finishInput(t, cold, ref, "continue")
	after, err := cold.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, after.Events, 8)
	require.Equal(t, history.Events, after.Events[:4])
	require.NotEqual(t, *history.SyncedEventID, *after.SyncedEventID)
	_, err = cold.StopRuntime(ctx(t), "agent")
	require.NoError(t, err)
	_, err = reg2.Get("agent")
	require.Error(t, err)
	t.Log("PASS actual SDK/fake: desired type/config, old Driver, new-session snapshot, native release gate, stop, cold material validation, original ID/ownership/history, no replay")
}

func replaceWorkspace(t *testing.T, path, work string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var scenario map[string]any
	require.NoError(t, json.Unmarshal(b, &scenario))
	var replace func(any)
	replace = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "cwd" {
					v[k] = work
				} else {
					replace(x)
				}
			}
		case []any:
			for _, x := range v {
				replace(x)
			}
		}
	}
	replace(scenario)
	b, err = json.Marshal(scenario)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0600))
	return path
}

func TestClaudeStopAndColdResume(t *testing.T) {
	dir := t.TempDir()
	svc, st, _ := lifecycleService(t, dir)
	path, work := nativeScenario(t, "claude", "basic")
	resumePath, _ := nativeScenario(t, "claude", "lifecycle-resume")
	replaceWorkspace(t, resumePath, work)
	spec := resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: spec})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	original, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "n", Name: "claude"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: original.Status.SessionID}
	finishInput(t, svc, ref, "hello")
	history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.NotEmpty(t, history.Events)
	spec.Config = runtime.Config{"executable": claudeMock, "scenario": resumePath}
	_, err = svc.UpdateRuntime("agent", spec)
	require.NoError(t, err)
	_, err = svc.StopRuntime(ctx(t), "agent")
	require.NoError(t, err)
	require.NoError(t, svc.Shutdown(ctx(t)))
	require.NoError(t, st.Close())
	cold, _, _ := lifecycleService(t, dir)
	r, err := cold.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	require.EqualValues(t, 2, r.Status.AppliedGeneration)
	resumed, err := cold.ResumeSession(ctx(t), ref)
	require.NoError(t, err)
	require.Equal(t, original.Status.SessionID, resumed.Status.SessionID)
	require.Equal(t, original.Metadata.UID, resumed.Metadata.UID)
	require.EqualValues(t, 2, resumed.Status.RuntimeGeneration)
	before, err := cold.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Equal(t, history, before)
	finishInput(t, cold, ref, "continue")
	after, err := cold.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Greater(t, len(after.Events), len(history.Events))
	require.Equal(t, history.Events, after.Events[:len(history.Events)])
	_, err = cold.ArchiveSession(ctx(t), ref)
	require.NoError(t, err)
	t.Log("PASS actual SDK/fake Claude ResumeID startup, observed init same ID/cwd, durable history prefix and separate new input; fake validates argv compatibility, not real native persistence")
}

func TestArchiveResumeKeepsObserversAndIdentity(t *testing.T) {
	svc, _, _ := lifecycleService(t, t.TempDir())
	path, work := nativeScenario(t, "codex", "archive-resume")
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	r, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "n", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: r.Status.SessionID}
	finishInput(t, svc, ref, "hello")
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	defer sub.Close()
	_, err = svc.ArchiveSession(ctx(t), ref)
	require.NoError(t, err)
	_, err = svc.ResumeSession(ctx(t), ref)
	require.NoError(t, err)
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []runtime.ContentBlock{runtime.Text("continue")}})
	require.NoError(t, err)
	message := false
	for {
		e, err := sub.Next(ctx(t))
		require.NoError(t, err)
		if p, ok := e.Payload().(runtime.AgentMessage); ok {
			require.Equal(t, "new answer", p.Content[0].Text)
			message = true
		}
		if p, ok := e.Payload().(runtime.StateUpdate); ok && p.State != nil && *p.State == runtime.Idle && message {
			break
		}
	}
	_, err = svc.ArchiveSession(ctx(t), ref)
	require.NoError(t, err)
	t.Log("PASS original live subscriber survives archive/resume and receives only subsequent live facts on the original persistent identity")
}
