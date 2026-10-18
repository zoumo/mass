// Command offline exercises the public Runtime using only temporary SDK mocks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := os.MkdirTemp("", "mass-offline-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Builds use the caller's Go toolchain. Agent children get only the explicit
	// scenario variable below: no inherited account environment or executable lookup.
	for _, kind := range []string{"codex", "claude"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		binary := filepath.Join(dir, "mock-"+kind)
		output, err := exec.CommandContext(ctx, "go", "build", "-o", binary,
			"github.com/zoumo/agent-sdk-go/"+kind+"/cmd/mock").CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("build public %s mock: %w: %s", kind, err, output)
		}
		scenario, err := writeScenario(dir, kind)
		if err != nil {
			return err
		}
		if err := exercise(kind, binary, scenario); err != nil {
			return err
		}
	}
	return nil
}

func exercise(kind, binary, scenario string) (result error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry := catalog.New()
	if len(registry.Types()) != 2 {
		return errors.New("unexpected system type catalog")
	}
	if _, err := registry.Describe(kind); err != nil {
		return err
	}
	_, err := registry.Register(rt.Registration{Name: "demo-" + kind, Type: kind,
		Config: rt.Config{Executable: binary, Env: []string{strings.ToUpper(kind) + "_MOCK_SCENARIO=" + scenario}}})
	if err != nil {
		return err
	}
	worker, err := registry.Get("demo-" + kind)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, worker.Stop()) }()
	if err = worker.Start(ctx); err != nil {
		return err
	}
	for _, feature := range worker.Status().Capabilities {
		if feature.Name == "create_session" && feature.Availability != "available" {
			return errors.New("create capability unavailable after activation")
		}
	}
	// Claude stays awaiting_input without a process. Codex allocates identity
	// without input. In either case only this explicit text is ever submitted.
	op, err := worker.CreateSession(ctx, rt.SessionOptions{})
	if err != nil {
		return err
	}
	if op.InputRequired {
		op, err = worker.ContinueCreation(ctx, op.OperationRef, "hello")
		if err != nil {
			return err
		}
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for op.Phase != "opened" {
		if op.Phase == "outcome_unknown" || op.Phase == "closed" {
			return fmt.Errorf("creation: %s (%s)", op.Phase, op.Failure)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		op, err = worker.GetOperation(op.OperationRef)
		if err != nil {
			return err
		}
	}
	if op.Key == nil {
		return errors.New("opened without native identity")
	}
	session, err := worker.GetSession(*op.Key)
	if err != nil {
		return err
	}
	if kind == "codex" {
		receipt, err := session.Submit(ctx, rt.TextInput("hello"))
		if err != nil {
			return err
		}
		fmt.Printf("%s: submission=%s nativeACK=%s\n", kind, receipt.Outcome, receipt.NativeAcknowledged.State)
	}
	var seq uint64
	for {
		event, err := session.Next(ctx)
		if err != nil {
			return err
		}
		if event.Seq != seq+1 {
			return errors.New("publication sequence gap")
		}
		seq = event.Seq
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var decoded rt.Event
		if err = json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		if decoded.SessionID != session.Key().SessionID {
			return errors.New("native identity changed")
		}
		if event.Kind == rt.TurnCompleted {
			break
		}
	}
	fmt.Printf("%s: native=%s managed=%d foreground=%s\n", kind, session.Key().SessionID, len(worker.ListSessions()), session.Snapshot().Foreground.State)
	// Native release is intentionally not relabelled success after local cleanup.
	receipt, err := worker.CloseSession(ctx, session.Key(), rt.CloseRelease)
	if !errors.Is(err, rt.ErrOutcomeUnknown) || receipt.Outcome != "close_unknown" {
		return fmt.Errorf("unexpected native release: %+v: %v", receipt, err)
	}
	fmt.Printf("%s: close=%s nativeRelease=%s\n", kind, receipt.Outcome, receipt.NativeRelease)
	stop, err := worker.StopWith(ctx, rt.StopOptions{Policy: rt.StopAbort})
	if err != nil {
		return err
	}
	fmt.Printf("%s: stop=%s incompleteFacts=%t\n", kind, stop.Outcome, stop.IncompleteFacts)
	var lifecycleSeq uint64
	opened, stopped := false, false
	for {
		event, err := worker.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		lifecycleSeq++
		if event.Seq != lifecycleSeq || event.SessionID != "" {
			return errors.New("invalid Runtime lifecycle envelope")
		}
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var decoded rt.Event
		if err = json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		if event.Kind == rt.SessionOpened {
			opened = event.Session != nil && event.Session.Key == session.Key()
		}
		if event.Kind == rt.RuntimeStatusChanged && event.Runtime.Phase == "stopped" {
			stopped = true
		}
	}
	if !opened || !stopped {
		return errors.New("missing typed Runtime lifecycle facts")
	}
	fmt.Printf("%s: lifecycle=observed capability=conditional\n", kind)
	return nil
}

func writeScenario(dir, kind string) (string, error) {
	var lines []string
	if kind == "codex" {
		lines = []string{
			`c2s {"id":1,"method":"initialize","params":{"clientInfo":{"name":"mass-go","version":"experiment"}}}`,
			`s2c {"id":1,"result":{"userAgent":"synthetic","codexHome":"/synthetic","platformFamily":"unix","platformOs":"linux"}}`,
			`c2s {"method":"initialized"}`,
			`c2s {"id":2,"method":"thread/start","params":{"ephemeral":true}}`,
			`s2c {"id":2,"result":{"thread":{"id":"native-thread","sessionId":"different-group","cliVersion":"synthetic","createdAt":1,"updatedAt":1,"cwd":"/synthetic","ephemeral":true,"modelProvider":"mock","preview":"","projectId":null,"source":"appServer","status":{"type":"idle"},"turns":[]},"approvalPolicy":"never","approvalsReviewer":"user","cwd":"/synthetic","model":"fake","modelProvider":"mock","sandbox":{"type":"readOnly"}}}`,
			`c2s {"id":3,"method":"turn/start","params":{"threadId":"native-thread","input":[{"type":"text","text":"hello","text_elements":[]}]}}`,
			`s2c {"id":3,"result":{"turn":{"id":"native-work","status":"inProgress","items":[],"error":null}}}`,
			`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-thread","turnId":"native-work","itemId":"native-message","delta":"synthetic reply"}}`,
			`s2c {"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-work","status":"completed","items":[],"error":null}}}`,
		}
	} else {
		lines = []string{
			`client control {"type":"control_request","request_id":"init","request":{"subtype":"initialize","hooks":null,"forwardSubagentText":true}}`,
			`server control {"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}`,
			`client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`,
			`server protocol {"type":"system","subtype":"init","session_id":"native-session"}`,
			`server protocol {"type":"assistant","message":{"id":"native-message","role":"assistant","content":[{"type":"text","text":"synthetic reply"}]}}`,
			`server protocol {"type":"result","subtype":"success","session_id":"native-session","is_error":false}`,
		}
	}
	frames := []map[string]any{}
	for i, line := range lines {
		direction, data, _ := strings.Cut(line, " ")
		frame := map[string]any{"ordinal": i + 1, "direction": direction, "channel": "protocol"}
		if kind == "codex" {
			frame["wire"] = json.RawMessage(data)
			frame["epoch"] = 1
		} else {
			channel, raw, _ := strings.Cut(data, " ")
			frame["channel"] = channel
			frame["data"] = json.RawMessage(raw)
		}
		frames = append(frames, frame)
	}
	scenario := map[string]any{"version": 1, "provenance": "synthetic", "frames": frames}
	if kind == "codex" {
		scenario["timeoutMs"] = 10000
	}
	data, err := json.Marshal(scenario)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, kind+".json")
	return path, os.WriteFile(path, data, 0600)
}
