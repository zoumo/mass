// massd is the executable in-process vertical slice, not an HTTP daemon yet.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func run() (result error) {
	data := flag.String("data", "", "absolute data directory")
	executable := flag.String("codex", "", "absolute SDK mock Codex executable")
	claudeExecutable := flag.String("claude", "", "absolute SDK mock Claude executable")
	scenario := flag.String("scenario", "", "absolute synthetic scenario")
	cwd := flag.String("cwd", "/synthetic", "native workspace (must match strict scenario)")
	input := flag.String("input", "hello", "independent text input")
	flag.Parse()
	st, err := file.Open(*data, false)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	kind := "codex"
	if *claudeExecutable != "" {
		if *executable != "" {
			return errors.New("choose exactly one mock executable")
		}
		kind = "claude"
		executable = claudeExecutable
	}
	svc := service.NewWithPolicy(st, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(*data, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(*data, "private")}}), prototype.AutoApprovePermissions)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result = errors.Join(result, svc.Shutdown(ctx))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: kind}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": *executable, "scenario": *scenario}}}); err != nil {
		return err
	}
	if _, err = svc.StartRuntime(ctx, kind); err != nil {
		return err
	}
	session, err := svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: kind, SessionSpec: runtime.SessionSpec{Cwd: *cwd}}})
	if err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	if err = out.Encode(session); err != nil {
		return err
	}
	ref := resource.SessionRef{SessionID: session.Status.SessionID}
	sub, err := svc.SubscribeEvents(ref)
	if err != nil {
		return err
	}
	defer sub.Close()
	receipt, err := svc.SendPrompt(ctx, ref, runtime.Input{Content: []runtime.ContentBlock{runtime.Text(*input)}})
	if err != nil {
		return err
	}
	if err = out.Encode(receipt); err != nil {
		return err
	}
	started := false
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if err = out.Encode(e); err != nil {
			return err
		}
		if p, ok := e.Payload().(runtime.StateUpdate); ok && p.State != nil {
			if *p.State == runtime.Running {
				started = true
			}
			if started && *p.State == runtime.Idle {
				return nil
			}
		}
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
