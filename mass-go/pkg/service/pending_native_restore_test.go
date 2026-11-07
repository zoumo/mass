package service_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

// The native peer accepts the explicit resume input but withholds identity.
// Fresh create establishes real adapter recovery material and durable facts.
func TestPendingNativeRestorePeer(t *testing.T) {
	dir := os.Getenv("MASS_PENDING_RESTORE_PEER")
	if dir == "" {
		return
	}
	resumed := false
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--resume=") {
			if arg != "--resume=restore-original" {
				os.Exit(5)
			}
			resumed = true
		}
	}
	phase := "create"
	if resumed {
		phase = "resume"
	}
	must := func(err error) {
		if err != nil {
			os.Exit(2)
		}
	}
	mark := func(name, value string) {
		must(os.WriteFile(filepath.Join(dir, phase+"-"+name), []byte(value), 0o600))
	}
	mark("pid", strconv.Itoa(os.Getpid()))
	start, err := restoreProcessStart(os.Getpid())
	must(err)
	mark("start", string(start))
	opened, err := os.OpenFile(filepath.Join(dir, "opened"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(err)
	_, err = fmt.Fprintf(opened, "%s %d\n", phase, os.Getpid())
	must(err)
	must(opened.Close())
	send := func(v any) {
		if e := json.NewEncoder(os.Stdout).Encode(v); e != nil {
			os.Exit(2)
		}
	}
	cwd, err := os.Getwd()
	must(err)
	init := func() {
		send(map[string]any{"type": "system", "subtype": "init", "session_id": "restore-original", "cwd": cwd})
	}
	resumeInput := make(chan string, 1)
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		must(json.Unmarshal(scan.Bytes(), &v))
		if v["type"] == "control_request" {
			r := v["request"].(map[string]any)
			if r["subtype"] != "initialize" {
				mark("unexpected", "non-initialize control")
				os.Exit(3)
			}
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
			mark("ack", "initialize acknowledged")
			if resumed {
				go func() {
					uuid := <-resumeInput
					for {
						if _, e := os.Stat(filepath.Join(dir, "release-init")); e == nil {
							init()
							send(map[string]any{"type": "assistant", "session_id": "restore-original", "user_message_uuid": uuid, "message": map[string]any{"id": "resume-answer", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "resume answer", "apiBlockIndex": 0}}}})
							send(map[string]any{"type": "result", "subtype": "success", "session_id": "restore-original", "user_message_uuid": uuid, "uuid": "resume-result"})
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
			}
			continue
		}
		if v["type"] != "user" {
			mark("unexpected", "unexpected input or replay")
			os.Exit(4)
		}
		content := v["message"].(map[string]any)["content"]
		if resumed {
			if content != "explicit resume input" {
				mark("unexpected", "wrong resume content")
				os.Exit(8)
			}
			if _, e := os.Stat(filepath.Join(dir, "resume-input")); e == nil {
				mark("unexpected", "duplicate resume input")
				os.Exit(9)
			}
			mark("input", "one explicit resume input")
			resumeInput <- v["uuid"].(string)
			continue
		}
		if content != "explicit original input" {
			mark("unexpected", "wrong initial content")
			os.Exit(6)
		}
		if _, e := os.Stat(filepath.Join(dir, "create-input")); e == nil {
			mark("unexpected", "duplicate initial input")
			os.Exit(7)
		}
		mark("input", "one explicit input")
		init()
		send(map[string]any{"type": "assistant", "session_id": "restore-original", "user_message_uuid": v["uuid"], "message": map[string]any{"id": "first-answer", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "durable original answer", "apiBlockIndex": 0}}}})
		send(map[string]any{"type": "result", "subtype": "success", "session_id": "restore-original", "user_message_uuid": v["uuid"], "uuid": "first-result"})
	}
	mark("eof", "stdin closed")
	os.Exit(0)
}

func TestPendingNativeRestoreOwnerStop(t *testing.T) {
	for _, mode := range []string{"stop", "shutdown", "caller-abandon-only"} {
		t.Run(mode, func(t *testing.T) { pendingNativeRestore(t, mode) })
	}
}

func pendingNativeRestore(t *testing.T, mode string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bin, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(bin)
	require.NoError(t, err)
	t.Logf("owned peer executable sha256=%x temporary root=%s", sha256.Sum256(binary), dir)
	peer := filepath.Join(dir, "peer")
	require.NoError(t, os.WriteFile(peer, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestPendingNativeRestorePeer$ -- \"$@\"\n", bin), 0o700))
	st, err := file.Open(filepath.Join(dir, "store"), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "recovery")}}))
	t.Cleanup(func() {
		// RED cleanup is external recovery, never proof of product Stop success.
		for _, phase := range []string{"create", "resume"} {
			data, e := os.ReadFile(filepath.Join(dir, phase+"-pid"))
			if e != nil {
				continue
			}
			pid, e := strconv.Atoi(string(data))
			if !assert.NoError(t, e) {
				continue
			}
			if syscall.Kill(pid, 0) == nil {
				want, e := os.ReadFile(filepath.Join(dir, phase+"-start"))
				if !assert.NoError(t, e) {
					continue
				}
				actual, e := restoreProcessStart(pid)
				if !assert.NoError(t, e) || !assert.Equal(t, string(want), string(actual), "never signal a reused PID") {
					continue
				}
				t.Logf("emergency cleanup, not product success: %s pid=%d", phase, pid)
				assert.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
			}
			assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond, "owned %s peer reaped pid=%d", phase, pid)
			t.Logf("resource readback: %s pid=%d absent", phase, pid)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, svc.Shutdown(cleanup))
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "pending"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": peer, "envPolicy": "replace", "env": map[string]any{"MASS_PENDING_RESTORE_PEER": dir}}}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = svc.StartRuntime(ctx, "pending")
	require.NoError(t, err)
	created, err := svc.CreateSessionWithInput(ctx, resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "pending"}, Spec: resource.SessionSpec{RuntimeRef: "pending", SessionSpec: runtime.SessionSpec{Cwd: dir}}, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("explicit original input")}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: created.Status.SessionID}
	require.Equal(t, "restore-original", ref.SessionID)
	var before store.HistoryPage
	require.Eventually(t, func() bool {
		before, err = svc.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
		r, e := svc.GetSession(ref)
		return err == nil && e == nil && r.Status.State == events.Idle && len(before.Events) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, "durable original answer", before.Events[0].Payload().(events.AgentMessage).Content[0].Text)
	closed, err := svc.CloseSession(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	before, err = svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	caller, abandon := context.WithCancel(ctx)
	defer abandon()
	resumed := make(chan error, 1)
	go func() {
		_, e := svc.ResumeSessionWithInput(caller, ref, runtime.Input{Content: []events.ContentBlock{events.Text("explicit resume input")}})
		resumed <- e
	}()
	require.Eventually(t, func() bool {
		select {
		case e := <-resumed:
			t.Fatalf("resume returned before the explicit input was observed: %v", e)
		default:
		}
		_, ackErr := os.Stat(filepath.Join(dir, "resume-ack"))
		_, inputErr := os.Stat(filepath.Join(dir, "resume-input"))
		return ackErr == nil && inputErr == nil
	}, 5*time.Second, 10*time.Millisecond, "restore must accept initialize and explicit input")
	data, err := os.ReadFile(filepath.Join(dir, "resume-pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err)
	identity, err := restoreProcessStart(pid)
	require.NoError(t, err)
	t.Logf("native restore initialize ACK/no init: pid=%d start=%s", pid, strings.TrimSpace(string(identity)))
	abandon()
	select {
	case e := <-resumed:
		require.ErrorContains(t, e, "lifecycle_wait_abandoned")
	case <-ctx.Done():
		t.Fatal("resume caller did not abandon wait")
	}
	require.Never(t, func() bool { return syscall.Kill(pid, 0) != nil }, 100*time.Millisecond, 10*time.Millisecond, "caller abandonment must retain native owner")
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("opening"), r.Status.State)
	_, err = svc.SendPrompt(ctx, ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch during opening")}})
	require.Error(t, err)
	if mode == "caller-abandon-only" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "release-init"), nil, 0o600))
		require.Eventually(t, func() bool {
			current, e := svc.GetSession(ref)
			return e == nil && current.Status.AppliedGeneration != 0
		}, 5*time.Second, 10*time.Millisecond, "original detached owner must accept late init without replacement resume")
	}
	stop, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if mode == "shutdown" {
		err = svc.Shutdown(stop)
	} else {
		_, err = svc.StopRuntime(stop, "pending")
	}
	r, readErr := svc.GetSession(ref)
	rt, rtErr := svc.GetRuntime("pending")
	require.NoError(t, err, "Stop must release original pending restore; session=%s runtime=%s", r.Status.State, rt.Status.State)
	require.NoError(t, readErr)
	require.NoError(t, rtErr)
	require.Equal(t, "stopped", rt.Status.State)
	if mode == "caller-abandon-only" {
		require.Equal(t, events.SessionState("offline"), r.Status.State)
		require.Equal(t, ref.SessionID, r.Status.SessionID)
	} else {
		require.Equal(t, closed.Status, r.Status)
	}
	require.Equal(t, closed.Metadata.UID, r.Metadata.UID)
	require.Equal(t, closed.Spec, r.Spec)
	after, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	if mode == "caller-abandon-only" {
		require.Len(t, after.Events, len(before.Events)+1)
		require.Equal(t, before.Events, after.Events[:len(before.Events)], "history identity/order/content must survive canceled restore")
		require.Equal(t, "resume answer", after.Events[len(before.Events)].Payload().(events.AgentMessage).Content[0].Text)
	} else {
		require.Equal(t, before, after, "history identity/order/content must survive canceled restore")
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
	require.FileExists(t, filepath.Join(dir, "resume-eof"))
	require.FileExists(t, filepath.Join(dir, "resume-input"))
	require.NoFileExists(t, filepath.Join(dir, "resume-unexpected"))
	opened, err := os.ReadFile(filepath.Join(dir, "opened"))
	require.NoError(t, err)
	require.Len(t, strings.Fields(string(opened)), 4, "exactly one fresh process and one original restore, never a retry")
	require.NoFileExists(t, filepath.Join(dir, "create-unexpected"))
}

func restoreProcessStart(pid int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
}

// Public Driver fault/timing fixture, not native capability evidence. A restore
// can succeed after cancellation; its subsequent close can report unknown release.
type pendingRestoreDriver struct {
	*recoveryDriver
	entered, canceled, release chan struct{}
	unknown                    bool
	unknownOpening             bool
	allowCleanup               atomic.Bool
	resumeCalls, promptCalls   atomic.Int32
}

func (d *pendingRestoreDriver) ResumeSession(ctx context.Context, id string, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.resumeCalls.Add(1)
	close(d.entered)
	<-ctx.Done()
	close(d.canceled)
	<-d.release
	if d.unknownOpening {
		return runtime.DriverSession{}, errors.New("pending restore release remains unknown")
	}
	source := events.NewSource()
	fact, err := events.NewEvent(events.EventBase{EventID: "19191919-1919-4191-8191-191919191919", Namespace: a.Namespace, SessionName: a.SessionName, SessionID: id, ItemID: "late-answer", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "late-answer", Content: []events.ContentBlock{events.Text("accepted late restore fact")}}, source)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	if err := d.pipe.Send(context.Background(), fact); err != nil {
		return runtime.DriverSession{}, err
	}
	return runtime.DriverSession{SessionID: id, Source: source}, nil
}

func TestPendingRestoreCancellationIsNotReleaseProof(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	// Seed an offline catalog entry through the public store interface. There is
	// no prior delivery lane or native process in this release-unknown fixture.
	original, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "original"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}, Status: resource.SessionStatus{SessionID: "original", State: "offline"}})
	require.NoError(t, err)
	d := &pendingRestoreDriver{recoveryDriver: &recoveryDriver{pipe: events.NewEventPipe(16)}, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), unknownOpening: true}
	release := sync.OnceFunc(func() { close(d.release) })
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fault": pendingRestoreFactory{d}}))
	t.Cleanup(func() {
		release()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The fixture owns no OS resources. End its stream even when the public
		// Service correctly refuses to claim its uncertain restore was released.
		e := svc.Shutdown(cleanup)
		d.pipe.End(nil)
		select {
		case <-d.entered:
			assert.Error(t, e)
		default:
			assert.NoError(t, e)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: original.Status.SessionID}
	resumed := make(chan error, 1)
	go func() { _, e := svc.ResumeSession(ctx, ref); resumed <- e }()
	select {
	case <-d.entered:
	case <-ctx.Done():
		t.Fatal("restore not entered")
	}
	release()
	_, err = svc.StopRuntime(ctx, "agent")
	require.ErrorContains(t, err, "original opening or stream responsibility remains")
	select {
	case e := <-resumed:
		require.ErrorContains(t, e, "pending restore release remains unknown")
	case <-ctx.Done():
		t.Fatal("restore did not return its unknown result")
	}
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, events.Unknown, r.Status.State)
	require.Equal(t, original.Metadata.UID, r.Metadata.UID)
	_, err = svc.ResumeSession(ctx, ref)
	require.Error(t, err, "no conflicting restore")
	_, err = svc.StartRuntime(ctx, "agent")
	require.Error(t, err, "no replacement Runtime")
	require.ErrorContains(t, svc.ReconcileOpening(ctx, "agent", runtime.Address{Namespace: "one", SessionName: "original"}), "native identity remains unknown")
	rt, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "stopping", rt.Status.State)
	require.Equal(t, int32(1), d.resumeCalls.Load())
	t.Log("resource ledger: fault fixture has no native process; uncertain original ownership retained, stream cleanup independent")
}

func (d *pendingRestoreDriver) CloseSession(context.Context, string) error {
	select {
	case <-d.entered:
		if d.unknown && !d.allowCleanup.Load() {
			return errors.New("restore cleanup release remains unknown")
		}
	default:
	}
	return nil
}

func (d *pendingRestoreDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	d.promptCalls.Add(1)
	return runtime.InputReceipt{}, errors.New("input must not pass Stop admission seal")
}

type pendingRestoreFactory struct{ d *pendingRestoreDriver }

func (f pendingRestoreFactory) Validate(runtime.Config) error { return nil }
func (f pendingRestoreFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.d, nil
}

func TestPendingRestoreStopLateSuccessAndUnknownRelease(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%t", unknown), func(t *testing.T) {
			st, err := file.Open(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, st.Close()) })
			d := &pendingRestoreDriver{recoveryDriver: &recoveryDriver{pipe: events.NewEventPipe(16)}, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), unknown: unknown}
			release := sync.OnceFunc(func() { close(d.release) })
			t.Cleanup(func() { d.pipe.End(nil) })
			svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fault": pendingRestoreFactory{d}}))
			t.Cleanup(func() {
				d.allowCleanup.Store(true)
				release()
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, svc.Shutdown(cleanup))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault", Config: runtime.Config{}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx, "agent")
			require.NoError(t, err)
			original, err := svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "original"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: original.Status.SessionID}
			_, err = svc.CloseSession(ctx, ref)
			require.NoError(t, err)
			resumed := make(chan error, 1)
			go func() { _, e := svc.ResumeSession(ctx, ref); resumed <- e }()
			select {
			case <-d.entered:
			case <-ctx.Done():
				t.Fatal("restore did not enter")
			}
			stopped := make(chan error, 2)
			for range 2 {
				go func() { _, e := svc.StopRuntime(ctx, "agent"); stopped <- e }()
			}
			select {
			case <-d.canceled:
			case <-ctx.Done():
				t.Fatal("Stop did not signal original restore before waiting admission")
			}
			_, err = svc.SendPrompt(ctx, ref, runtime.Input{Content: []events.ContentBlock{events.Text("sealed input")}})
			require.ErrorContains(t, err, "session unavailable")
			_, err = svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "forbidden"}, Spec: original.Spec})
			require.Error(t, err, "Stop admission seal must reject new creation")
			release()
			for range 2 {
				select {
				case e := <-stopped:
					if unknown {
						require.ErrorContains(t, e, "restore cleanup release remains unknown")
					} else {
						require.NoError(t, e)
					}
				case <-ctx.Done():
					t.Fatal("original Stop did not settle")
				}
			}
			select {
			case e := <-resumed:
				require.NoError(t, e, "a late native result still belongs to the original sealed owner")
			case <-ctx.Done():
				t.Fatal("restore owner did not settle")
			}
			r, err := svc.GetSession(ref)
			require.NoError(t, err)
			rt, err := svc.GetRuntime("agent")
			require.NoError(t, err)
			if unknown {
				require.Equal(t, events.SessionState("closing"), r.Status.State, "unconfirmed close must retain original release responsibility")
				require.Equal(t, "stopping", rt.Status.State)
				_, err = svc.ResumeSession(ctx, ref)
				require.Error(t, err, "unknown release cannot admit replacement restore")
				_, err = svc.StartRuntime(ctx, "agent")
				require.Error(t, err, "unknown release cannot restart runtime")
				d.allowCleanup.Store(true)
				_, err = svc.StopRuntime(ctx, "agent")
				require.NoError(t, err, "explicit cleanup retry must settle original resource, not resume again")
			} else {
				require.Equal(t, events.SessionState("offline"), r.Status.State)
				require.Equal(t, "stopped", rt.Status.State)
				_, err = svc.SendPrompt(ctx, ref, runtime.Input{Content: []events.ContentBlock{events.Text("no reopened lane")}})
				require.Error(t, err)
			}
			page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
			require.NoError(t, err)
			require.Len(t, page.Events, 1, "late accepted fact must drain before Stop success, including explicit cleanup retry")
			require.Equal(t, "19191919-1919-4191-8191-191919191919", page.Events[0].EventID)
			require.Equal(t, "accepted late restore fact", page.Events[0].Payload().(events.AgentMessage).Content[0].Text)
			require.Equal(t, int32(1), d.resumeCalls.Load(), "no replacement native restore")
			require.Zero(t, d.promptCalls.Load(), "no native input dispatch while stopping or stopped")
		})
	}
}
