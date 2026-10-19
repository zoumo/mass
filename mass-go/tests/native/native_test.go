//go:build native

// Package native_test exercises installed Agent CLIs through public Go APIs.
// This group is opt-in: go test -tags=native ./tests/native.
package native_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type diskBindings struct{ dir string }

func (b diskBindings) path(k rt.SessionKey) string {
	data, _ := json.Marshal(k)
	hash := sha256.Sum256(data)
	return filepath.Join(b.dir, hex.EncodeToString(hash[:])+".json")
}
func (b diskBindings) Save(ctx context.Context, k rt.SessionKey, v rt.RecoveryBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir, "binding-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), b.path(k))
}
func (b diskBindings) Load(ctx context.Context, k rt.SessionKey) (rt.RecoveryBinding, error) {
	var v rt.RecoveryBinding
	if err := ctx.Err(); err != nil {
		return v, err
	}
	data, err := os.ReadFile(b.path(k))
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(data, &v)
	return v, err
}

type fixture struct {
	t         *testing.T
	kind, dir string
	worker    rt.Runtime
	mu        sync.Mutex
	log       *os.File
}

func evidenceDir(t *testing.T, kind string) string {
	t.Helper()
	root := os.Getenv("MASS_NATIVE_EVIDENCE")
	if !filepath.IsAbs(root) {
		t.Fatal("MASS_NATIVE_EVIDENCE must be an absolute retained evidence directory")
	}
	dir, err := os.MkdirTemp(root, kind+"-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func newFixture(t *testing.T, kind, dir string, bindings bool) *fixture {
	t.Helper()
	binary := os.Getenv("MASS_NATIVE_" + strings.ToUpper(kind))
	if !filepath.IsAbs(binary) {
		t.Fatalf("MASS_NATIVE_%s must be an absolute real CLI", strings.ToUpper(kind))
	}
	work := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bindings"), 0700); err != nil {
		t.Fatal(err)
	}
	// Test-helper dispatch is not part of the original account/config scope.
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MASS_NATIVE_") && !strings.HasPrefix(entry, "CLAUDECODE=") {
			env = append(env, entry)
		}
	}
	registry := catalog.New()
	if bindings {
		registry = catalog.NewWithBindings(diskBindings{filepath.Join(dir, "bindings")})
	}
	_, err := registry.Register(rt.Registration{Name: "native-" + kind, Type: kind, Config: rt.Config{Executable: binary, Dir: work, Env: env, CreationLimits: rt.CreationLimits{Timeout: 15 * time.Second}, DeliveryLimits: rt.DeliveryLimits{Facts: 4096, Bytes: 16 << 20}, LifecycleLimits: rt.DeliveryLimits{Facts: 4096, Bytes: 16 << 20}}})
	if err != nil {
		t.Fatal(err)
	}
	w, err := registry.Get("native-" + kind)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", os.Getpid())), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, kind: kind, dir: dir, worker: w, log: log}
	t.Cleanup(func() {
		err := w.Stop()
		f.record("stop", map[string]any{"error": fmt.Sprint(err), "status": w.Status(), "sessions": w.ListSessions()})
		if err != nil {
			t.Error(err)
		}
		log.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.record("start", map[string]any{"pid": os.Getpid(), "executable": binary, "runtime": w.Status()})
	t.Log("retained evidence:", dir)
	return f
}
func (f *fixture) record(kind string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := json.NewEncoder(f.log).Encode(map[string]any{"at": time.Now().UTC(), "kind": kind, "value": value}); err != nil {
		f.t.Error(err)
	}
}
func (f *fixture) create(ctx context.Context, input string) (rt.Session, error) {
	op, err := f.worker.CreateSession(ctx, rt.SessionOptions{InitialInput: &input})
	if err != nil {
		return nil, err
	}
	for op.Phase != "opened" {
		if op.Phase == "closed" || op.Phase == "outcome_unknown" {
			f.record("creation_failed", op)
			return nil, fmt.Errorf("creation: %s %s", op.Phase, op.Failure)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		op, err = f.worker.GetOperation(op.OperationRef)
		if err != nil {
			return nil, err
		}
	}
	f.record("opened", op)
	return f.worker.GetSession(*op.Key)
}

type observation struct {
	text                     string
	childText                bool
	relations, childTerminal int
	started, completed       time.Time
}

func (f *fixture) consume(ctx context.Context, s rt.Session, seq *uint64) (observation, error) {
	var o observation
	for {
		e, err := s.Next(ctx)
		if err != nil {
			return o, err
		}
		f.record("event", e)
		if e.SessionID != s.Key().SessionID || e.Seq != *seq+1 {
			return o, errors.New("session identity or sequence mismatch")
		}
		*seq = e.Seq
		wire, err := json.Marshal(e)
		if err != nil {
			return o, err
		}
		var decoded rt.Event
		if err = json.Unmarshal(wire, &decoded); err != nil {
			return o, err
		}
		child := e.Origin.Attribution == "child"
		if child && e.OperationRef != "" {
			return o, errors.New("child acquired root submission authority")
		}
		if e.Kind == rt.AgentAssociated || e.Kind == rt.AgentCreated {
			o.relations++
		}
		if child && e.AgentState != nil && e.AgentState.Terminal {
			o.childTerminal++
		}
		if e.Message != nil && e.Message.Role == "assistant" {
			for _, b := range e.Message.Content.Blocks {
				if b.Text != nil {
					o.text += *b.Text
					if child && strings.Contains(*b.Text, "CHILD_PROOF_7391") {
						o.childText = true
					}
				}
			}
		}
		if e.Kind == rt.TurnStarted {
			o.started = e.ObservedAt
		}
		if e.Kind == rt.InteractionRequested {
			if e.Interaction.Kind != "permission" {
				return o, errors.New("unexpected question interaction")
			}
			r, err := s.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
			f.record("response", r)
			if err != nil {
				return o, err
			}
		}
		if e.Kind == rt.Failure {
			return o, fmt.Errorf("native event failure: %+v", e.Error)
		}
		if e.Kind == rt.TurnCompleted {
			if e.Work == nil || e.Work.Status != "completed" {
				return o, fmt.Errorf("root terminal: %+v", e.Work)
			}
			o.completed = e.ObservedAt
			return o, nil
		}
	}
}

func TestConcurrentSessions(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, kind, evidenceDir(t, kind+"-concurrent"), false)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			var sessions [2]rt.Session
			var results [2]observation
			var errs [2]error
			var seq [2]uint64
			var admitted [2]time.Time
			barrier := make(chan struct{})
			var wg sync.WaitGroup
			for i := range sessions {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-barrier
					s, err := f.create(ctx, fmt.Sprintf("Remember this session's unique marker CONCURRENT_%d_4827. Think briefly, then reply exactly that marker. Do not use tools.", i))
					sessions[i] = s
					if err == nil {
						admitted[i] = time.Now().UTC()
						results[i], err = f.consume(ctx, s, &seq[i])
					}
					errs[i] = err
				}(i)
			}
			close(barrier)
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("session %d: %v", i, err)
				}
				if !strings.Contains(results[i].text, fmt.Sprintf("CONCURRENT_%d_4827", i)) || strings.Contains(results[i].text, fmt.Sprintf("CONCURRENT_%d_4827", 1-i)) {
					t.Fatalf("cross-session reply: %+v", results[i])
				}
			}
			if sessions[0].Key() == sessions[1].Key() {
				t.Fatal("native identity reused")
			}
			if !admitted[0].Before(results[1].completed) || !admitted[1].Before(results[0].completed) {
				t.Fatal("sessions did not overlap before foreground completion")
			}
			f.record("concurrent_verified", map[string]any{"first": sessions[0].Key(), "second": sessions[1].Key(), "observations": []map[string]any{{"started": results[0].started, "completed": results[0].completed}, {"started": results[1].started, "completed": results[1].completed}}})
			if _, err := f.worker.CloseSession(ctx, sessions[0].Key(), rt.CloseDetach); err != nil {
				t.Fatal(err)
			}
			r, err := sessions[1].Submit(ctx, rt.TextInput("Reply with only the unique marker from my first message, without using tools."))
			f.record("survivor_submit", r)
			if err != nil {
				t.Fatal(err)
			}
			o, err := f.consume(ctx, sessions[1], &seq[1])
			if err != nil || !strings.Contains(o.text, "CONCURRENT_1_4827") || strings.Contains(o.text, "CONCURRENT_0_4827") {
				t.Fatalf("survivor: %+v: %v", o, err)
			}
		})
	}
}

func TestSubagents(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, kind, evidenceDir(t, kind+"-subagent"), false)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			if err := os.WriteFile(filepath.Join(f.dir, "workspace", "child.txt"), []byte("CHILD_PROOF_7391\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := f.create(ctx, "This is a real subagent integration test. You must use the native Agent/Task or spawn_agent tool to create exactly one child agent. Have that child read child.txt in the current directory and reply with the exact marker it contains. Parent must not read the file itself. Wait for the child to finish, then report its result. Do not use background shell jobs as a substitute. Do not access files outside this test directory.")
			if err != nil {
				t.Fatal(err)
			}
			var seq uint64
			o, err := f.consume(ctx, s, &seq)
			f.record("subagent_observation", map[string]any{"relations": o.relations, "childText": o.childText, "childTerminal": o.childTerminal, "error": fmt.Sprint(err)})
			if err != nil {
				t.Fatal(err)
			}
			if o.relations == 0 || !o.childText || o.childTerminal == 0 {
				t.Fatalf("missing observed child relationship/content/terminal: relations=%d content=%t terminal=%d", o.relations, o.childText, o.childTerminal)
			}
		})
	}
}

// Each stage runs in a separate test host process. Only the native key and
// opaque host bindings cross the boundary; the original Runtime is destroyed.
func TestColdRecovery(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			dir := evidenceDir(t, kind+"-cold")
			for _, stage := range []string{"create", "resume"} {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestColdWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "MASS_NATIVE_HELPER=1", "MASS_NATIVE_KIND="+kind, "MASS_NATIVE_STAGE="+stage, "MASS_NATIVE_DIR="+dir)
				output, err := cmd.CombinedOutput()
				cancel()
				if writeErr := os.WriteFile(filepath.Join(dir, stage+".log"), output, 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
				t.Logf("%s: %s", stage, output)
				if err != nil {
					t.Fatalf("cold %s: %v; evidence: %s", stage, err, dir)
				}
			}
		})
	}
}
func TestColdWorker(t *testing.T) {
	if os.Getenv("MASS_NATIVE_HELPER") != "1" {
		t.Skip("only invoked by cold recovery parent")
	}
	kind, dir, stage := os.Getenv("MASS_NATIVE_KIND"), os.Getenv("MASS_NATIVE_DIR"), os.Getenv("MASS_NATIVE_STAGE")
	f := newFixture(t, kind, dir, true)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	keyPath := filepath.Join(dir, "key.json")
	if stage == "create" {
		s, err := f.create(ctx, "Remember the exact marker COLD_MEMORY_9362. Reply exactly that marker without tools.")
		if err != nil {
			t.Fatal(err)
		}
		var seq uint64
		o, err := f.consume(ctx, s, &seq)
		if err != nil || !strings.Contains(o.text, "COLD_MEMORY_9362") {
			t.Fatalf("initial: %v", err)
		}
		data, _ := json.Marshal(s.Key())
		if err = os.WriteFile(keyPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		r, err := f.worker.CloseSession(ctx, s.Key(), rt.CloseDetach)
		f.record("detach", r)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var key rt.SessionKey
	if err = json.Unmarshal(data, &key); err != nil {
		t.Fatal(err)
	}
	s, err := f.worker.ResumeSession(ctx, key)
	f.record("resume", map[string]any{"key": key, "error": fmt.Sprint(err), "sessions": f.worker.ListSessions()})
	if err != nil {
		t.Fatalf("identity-verified cold resume failed: %v", err)
	}
	if s.Key() != key {
		t.Fatal("resume substituted native identity")
	}
	r, err := s.Submit(ctx, rt.TextInput("What exact marker did I ask you to remember in the previous conversation? Reply only that marker without tools."))
	f.record("resume_submit", r)
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	o, err := f.consume(ctx, s, &seq)
	if err != nil || !strings.Contains(o.text, "COLD_MEMORY_9362") {
		t.Fatalf("resumed memory: %+v: %v", o, err)
	}
}
