// Command sdksimulation runs only the two-text continuation case through MASS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	historyfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	metafile "github.com/zoumo/mass/mass-go/pkg/metastore/file"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type request struct {
	Case, Materials, Workspace, Output string
	Peer                               struct {
		Command string
		Args    []string
	}
}

type plan struct {
	CaseID, Provider, DriverProfile, Project string
	Prompts                                  []string
}

func readJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func input(text string) runtime.Input {
	return runtime.Input{Content: []events.ContentBlock{{Type: "text", Text: text}}}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func run(ctx context.Context, req request) (err error) {
	var p plan
	if err := readJSON(filepath.Join(req.Materials, "case.json"), &p); err != nil {
		return err
	}
	if req.Case != "SDK-CASE-001" || p.CaseID != req.Case || p.DriverProfile != "continuation" || len(p.Prompts) != 2 {
		return errors.New("only SDK-CASE-001 continuation with exactly two prompts is supported")
	}
	if p.Provider != "codex" && p.Provider != "claude" {
		return errors.New("unsupported provider")
	}
	if !filepath.IsAbs(req.Peer.Command) || !filepath.IsAbs(p.Project) {
		return errors.New("absolute runner peer and recorded project required")
	}
	// Claude accepts an executable path only. Forward every supplied peer argument,
	// followed by native SDK arguments; never discover a host Agent executable.
	launcher := filepath.Join(req.Output, "mass-peer")
	args := []string{shellQuote(req.Peer.Command)}
	for _, arg := range req.Peer.Args {
		args = append(args, shellQuote(arg))
	}
	line := "#!/bin/sh\nexec " + strings.Join(args, " ")
	if err := os.WriteFile(launcher, []byte(line+" -- \"$@\" 2>"+shellQuote(filepath.Join(req.Output, "peer-stderr.log"))+"\n"), 0o700); err != nil {
		return err
	}
	meta, err := metafile.Open(filepath.Join(req.Output, "metadata"), false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, meta.Close()) }()
	history, err := historyfile.Open(filepath.Join(req.Output, "history"), false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, history.Close()) }()
	registry := runtime.NewRegistry(map[string]runtime.Factory{
		"codex":  codex.Factory{RecoveryDir: filepath.Join(req.Output, "recovery")},
		"claude": claude.Factory{RecoveryDir: filepath.Join(req.Output, "recovery")},
	})
	svc := service.New(meta, history, registry)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupErr := svc.Shutdown(cleanup)
		err = errors.Join(err, cleanupErr, writeJSON(filepath.Join(req.Output, "cleanup.json"), map[string]any{"released": cleanupErr == nil, "error": fmt.Sprint(cleanupErr)}))
	}()
	config := runtime.Config{"executable": launcher}
	if p.Provider == "codex" {
		config["args"] = []any{}
	}
	if _, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "recording"}, Spec: resource.RuntimeSpec{Type: p.Provider, Config: config}}); err != nil {
		return fmt.Errorf("create runtime: %w", err)
	}
	if _, err = svc.StartRuntime(ctx, "recording"); err != nil {
		return fmt.Errorf("start runtime: %w", err)
	}
	created, err := svc.CreateSessionWithInput(ctx, resource.SessionCreateInput{
		Metadata:     resource.Metadata{Namespace: "simulation", Name: "continuation"},
		Spec:         resource.SessionSpec{RuntimeRef: "recording", SessionSpec: runtime.SessionSpec{Cwd: p.Project}},
		InitialInput: ptrInput(p.Prompts[0]),
	})
	if err != nil {
		return fmt.Errorf("create session with initial input: %w", err)
	}
	if created.Status.SessionID == "" || created.InitialInputReceipt == nil {
		return errors.New("missing native identity or initial receipt")
	}
	if err := writeJSON(filepath.Join(req.Output, "created.json"), created); err != nil {
		return err
	}
	ref := resource.SessionRef{SessionID: created.Status.SessionID}
	// Initial events may precede the create response. Read public committed
	// history and Session state so that those events cannot be missed.
	for turn := 1; turn <= 2; turn++ {
		if err := awaitTurns(ctx, svc, ref, turn, req.Output); err != nil {
			return err
		}
		if turn == 1 {
			receipt, err := svc.SendPrompt(ctx, ref, input(p.Prompts[1]))
			if err != nil {
				return fmt.Errorf("second input on original session: %w", err)
			}
			if receipt.Submission == "" {
				return errors.New("missing second submission receipt")
			}
			if err := writeJSON(filepath.Join(req.Output, "second-receipt.json"), receipt); err != nil {
				return err
			}
		}
	}
	current, err := svc.GetSession(ref)
	if err != nil {
		return err
	}
	if current.Status.SessionID != created.Status.SessionID {
		return errors.New("continuation changed native identity")
	}
	_, err = svc.StopRuntime(ctx, "recording")
	return err
}

func ptrInput(text string) *runtime.Input { v := input(text); return &v }

func awaitTurns(ctx context.Context, svc *service.Service, ref resource.SessionRef, count int, output string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{Limit: 256})
		if err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(output, "mass-history.json"), page); err != nil {
			return err
		}
		if page.Next != "" {
			return errors.New("bounded case history exceeded one page")
		}
		messages := 0
		for _, event := range page.Events {
			if event.SessionID != ref.SessionID || event.Namespace != "simulation" || event.SessionName != "continuation" {
				return errors.New("MASS event routing mismatch")
			}
			switch v := event.Payload().(type) {
			case events.AgentMessage:
				for _, b := range v.Content {
					if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
						messages++
						break
					}
				}
			case events.SessionError:
				return fmt.Errorf("MASS session error: %v", v)
			}
		}
		current, err := svc.GetSession(ref)
		if err != nil {
			return err
		}
		if current.Status.State == events.Idle && messages == count {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("await %d completed MASS turns: %w", count, ctx.Err())
		case <-ticker.C:
		}
	}
}

func execute() error {
	path := flag.String("request", "", "runner request JSON")
	flag.Parse()
	var req request
	if err := readJSON(*path, &req); err != nil {
		return err
	}
	if err := os.MkdirAll(req.Output, 0o700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	status, message := "passed", "two completed nonempty MASS turns on one native session; runtime stopped"
	if err := run(ctx, req); err != nil {
		status, message = "failed", err.Error()
	}
	return writeJSON(filepath.Join(req.Output, "result.json"), map[string]string{"status": status, "message": message})
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
