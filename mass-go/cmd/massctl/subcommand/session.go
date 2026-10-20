package subcommand

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (o *options) sessionCommands() *cobra.Command {
	root := group("session", "Manage Sessions and observe future events")
	for _, action := range []string{"create", "get", "list", "resume", "archive", "cancel", "prompt", "history", "watch"} {
		cmd := &cobra.Command{Use: action + " NAME_OR_ID", Short: action + " Session", Args: cobra.ExactArgs(1)}
		cmd.Long = "Use NAME -n NAMESPACE or a raw global Session ID without namespace. No default namespace or lookup fallback."
		cmd.Example = "  massctl session " + action + " review -n demo\n  massctl session " + action + " SESSION_ID"
		var namespace, runtimeRef, cwd, systemPrompt, systemFile, text, after string
		var dirs []string
		var limit int
		if action == "list" {
			cmd.Long = "List one page across all namespaces and Runtimes. --after accepts the previous next cursor."
			cmd.Example = "  massctl session list --limit 100"
			cmd.Use = "list"
			cmd.Args = cobra.NoArgs
		} else {
			cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Session namespace; omit to address by ID")
		}
		if action == "create" {
			cmd.Use = "create NAME"
			cmd.Long = "NAME, --namespace, --runtime and --cwd are required. --system-prompt and --system-prompt-file are mutually exclusive. File '-' reads stdin; --additional-dir is repeatable. Creation does not send input. Paths refer to the Agent host."
			cmd.Example = "  massctl session create review -n demo --runtime agent --cwd /remote/workspace"
			cmd.Flags().StringVar(&runtimeRef, "runtime", "", "Runtime name (required)")
			cmd.Flags().StringVar(&cwd, "cwd", "", "Agent working directory (required)")
			cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt text")
			cmd.Flags().StringVar(&systemFile, "system-prompt-file", "", "UTF-8 system prompt file (- for stdin)")
			cmd.Flags().StringArrayVar(&dirs, "additional-dir", nil, "Additional Agent directory (repeatable)")
			cmd.MarkFlagsMutuallyExclusive("system-prompt", "system-prompt-file")
			for _, flag := range []string{"namespace", "runtime", "cwd"} {
				_ = cmd.MarkFlagRequired(flag)
			}
		}
		if action == "prompt" {
			cmd.Example = "  massctl session prompt review -n demo --text 'Describe the workspace'"
			cmd.Flags().StringVar(&text, "text", "", "Text input (required)")
			_ = cmd.MarkFlagRequired("text")
		}
		if action == "watch" {
			cmd.Long += " Observe only future events after subscription.ready on stderr; no history or reconnect. Ctrl-C closes this subscription without canceling Agent work."
		}
		if action == "list" || action == "history" {
			cmd.Flags().IntVar(&limit, "limit", 100, "Page size (positive)")
			cmd.Flags().StringVar(&after, "after", "", "Opaque next-page cursor")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("namespace") && namespace == "" {
				return invalid("Namespace must not be empty")
			}
			if (action == "list" || action == "history") && limit < 1 {
				return invalid("Limit must be positive")
			}
			p := client.Params{}
			if len(args) > 0 {
				if !utf8.ValidString(args[0]) || args[0] == "" || args[0] == "." || args[0] == ".." {
					return invalid("Invalid Session target")
				}
				if cmd.Flags().Changed("namespace") {
					p.Session = resource.SessionRef{Namespace: namespace, Name: args[0]}
				} else {
					p.Session = resource.SessionRef{SessionID: args[0]}
				}
			}
			switch action {
			case "create":
				if namespace == "" || runtimeRef == "" || cwd == "" {
					return invalid("Namespace, runtime and cwd are required")
				}
				if cmd.Flags().Changed("system-prompt-file") {
					b, err := readFile(cmd, systemFile)
					if err != nil {
						return err
					}
					systemPrompt = string(b)
				}
				p.Create = &resource.Session{Metadata: resource.Metadata{Namespace: namespace, Name: args[0]}, Spec: resource.SessionSpec{RuntimeRef: runtimeRef, SessionSpec: runtime.SessionSpec{Cwd: cwd, SystemPrompt: systemPrompt, AdditionalDirs: dirs}}}
			case "list":
				p.SessionQuery = resource.SessionQuery{Limit: limit, After: after}
			case "history":
				p.History = store.HistoryQuery{Limit: limit, After: after}
			case "prompt":
				p.Input = runtime.Input{Content: []events.ContentBlock{events.Text(text)}}
			case "watch":
				return o.watch(cmd, p.Session)
			}
			op := action
			if op == "prompt" {
				op = "input"
			}
			return o.result(cmd, "session."+op, p)
		}
		root.AddCommand(cmd)
	}
	return root
}

func (o *options) watch(cmd *cobra.Command, ref resource.SessionRef) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	var timer *time.Timer
	var mu sync.Mutex
	ready := false
	if o.timeout > 0 {
		timer = time.AfterFunc(o.timeout, func() {
			mu.Lock()
			defer mu.Unlock()
			if !ready {
				cancel()
			}
		})
		defer timer.Stop()
	}
	observer, err := o.client.Observe(ctx, ref)
	if err != nil {
		return requestError(err, "watch")
	}
	defer observer.Close()
	mu.Lock()
	ready = true
	expired := ctx.Err() != nil
	mu.Unlock()
	if expired {
		return requestError(ctx.Err(), "watch")
	}
	if timer != nil {
		timer.Stop()
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "subscription.ready"); err != nil {
		return err
	}
	for {
		event, err := observer.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return requestError(err, "watch")
		}
		raw, err := event.MarshalJSON()
		if err != nil {
			return requestError(err, "watch")
		}
		if err := o.print(cmd.OutOrStdout(), raw); err != nil {
			return err
		}
	}
}
