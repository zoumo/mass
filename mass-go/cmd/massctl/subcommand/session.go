package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (o *options) sessionCommands() *cobra.Command {
	root := group("session", "Manage Sessions and observe future events")
	for _, action := range []string{"create", "load", "get", "list", "close", "resume", "cancel", "cancel-load", "prompt", "history", "watch"} {
		cmd := &cobra.Command{Use: action + " NAME_OR_ID", Short: action + " Session", Args: cobra.ExactArgs(1)}
		cmd.Long = "Use NAME -n NAMESPACE or a raw global Session ID without namespace. No default namespace or lookup fallback."
		cmd.Example = "  massctl session " + action + " review -n demo\n  massctl session " + action + " SESSION_ID"
		var namespace, runtimeRef, cwd, systemPrompt, systemFile, text, after, inputFile string
		var nativeSessionID, loadID string
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
		var optionsInput sessionOptionsInput
		var initialConfig runtime.InitialSessionConfig
		if action == "create" || action == "load" {
			cmd.Flags().StringVar(&initialConfig.Model, "model", "", "Initial native model (literal; empty omits)")
			cmd.Flags().StringVar(&initialConfig.Effort, "effort", "", "Initial native effort (literal; empty omits)")
		}
		if action == "create" {
			cmd.Use = "create NAME"
			cmd.Long = "NAME, --namespace, --runtime and --cwd are required. --system-prompt and --system-prompt-file are mutually exclusive. File '-' reads stdin; --additional-dir is repeatable. --model and --effort save literal initial configuration for create and resume. --features-json and --mcp-servers-json override Runtime Session defaults by name. Creation sends no input unless --text or --input-file is supplied; the receipt is not turn completion. Paths refer to the Agent host."
			cmd.Example = "  massctl session create review -n demo --runtime agent --cwd /remote/workspace\n  massctl session create review -n demo --runtime agent --cwd /remote/workspace --features-json '{\"agentQuestions\":false}'"
			cmd.Flags().StringVar(&runtimeRef, "runtime", "", "Runtime name (required)")
			cmd.Flags().StringVar(&cwd, "cwd", "", "Agent working directory (required)")
			cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt text")
			cmd.Flags().StringVar(&systemFile, "system-prompt-file", "", "UTF-8 system prompt file (- for stdin)")
			cmd.Flags().StringArrayVar(&dirs, "additional-dir", nil, "Additional Agent directory (repeatable)")
			optionsInput.flags(cmd)
			cmd.MarkFlagsMutuallyExclusive("system-prompt", "system-prompt-file")

			for _, flag := range []string{"namespace", "runtime", "cwd"} {
				_ = cmd.MarkFlagRequired(flag)
			}
		}
		if action == "create" || action == "resume" {
			cmd.Flags().StringVar(&text, "text", "", "New explicit user text for this operation only")
			cmd.Flags().StringVar(&inputFile, "input-file", "", "UTF-8 SessionInput JSON file (- for stdin, never replayed)")
			cmd.MarkFlagsMutuallyExclusive("text", "input-file")
		}
		if action == "resume" {
			cmd.Long += " Optional --text or --input-file supplies one NEW input, not history replay. Success requires observed original identity; a receipt is not turn completion."
		}

		if action == "load" {
			cmd.Use = "load NAME"
			cmd.Long = "Accept an asynchronous load into a new Session. Namespace, runtime, cwd and native session ID are required. --model and --effort save literal initial configuration for this native resume and future resumes. --features-json and --mcp-servers-json provide Session overrides merged with current Runtime defaults. The Runtime must already be started. Paths refer to the Agent host. The receipt is acceptance only; inspect session get -o json for historyLoad status. Timeout or interruption ends waiting, not the accepted load."
			cmd.Example = "  massctl session load imported -n demo --runtime agent --cwd /remote/workspace --session-id NATIVE_ID"
			cmd.Flags().StringVar(&runtimeRef, "runtime", "", "Started Runtime name (required)")
			cmd.Flags().StringVar(&cwd, "cwd", "", "Agent working directory (required)")
			cmd.Flags().StringVar(&nativeSessionID, "session-id", "", "Raw native Session ID (required)")
			optionsInput.flags(cmd)
			for _, flag := range []string{"namespace", "runtime", "cwd", "session-id"} {
				_ = cmd.MarkFlagRequired(flag)
			}
		}
		if action == "cancel-load" {
			cmd.Long += " Cancel only the original load identified by --load-id, not subsequent live work. The returned status does not prove cleanup is complete."
			cmd.Example = "  massctl session cancel-load imported -n demo --load-id ORIGINAL_LOAD_ID\n  massctl session cancel-load SESSION_ID --load-id ORIGINAL_LOAD_ID"
			cmd.Flags().StringVar(&loadID, "load-id", "", "Original opaque load ID (required)")
			_ = cmd.MarkFlagRequired("load-id")
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
			ref := resource.SessionRef{}
			var call func(context.Context, any) error
			if len(args) > 0 {
				if !utf8.ValidString(args[0]) || args[0] == "" || args[0] == "." || args[0] == ".." {
					return invalid("Invalid Session target")
				}
				if cmd.Flags().Changed("namespace") {
					ref = resource.SessionRef{Namespace: namespace, Name: args[0]}
				} else {
					ref = resource.SessionRef{SessionID: args[0]}
				}
			}
			if systemFile == "-" && inputFile == "-" {
				return invalid("Only one file may read stdin")
			}
			var initial *runtime.Input
			if cmd.Flags().Changed("text") {
				initial = &runtime.Input{Content: []events.ContentBlock{events.Text(text)}}
			}
			if cmd.Flags().Changed("input-file") {
				raw, err := readFile(cmd, inputFile)
				if err != nil {
					return err
				}
				var input runtime.Input
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err = decoder.Decode(&input); err != nil {
					return invalid("Invalid SessionInput JSON")
				}
				if decoder.Decode(new(any)) != io.EOF {
					return invalid("Expected one SessionInput JSON value")
				}
				initial = &input
			}
			var config *runtime.InitialSessionConfig
			if action == "create" || action == "load" {
				if err := initialConfig.Validate(); err != nil {
					return invalid("Invalid initial Session config")
				}
				if cmd.Flags().Changed("model") || cmd.Flags().Changed("effort") {
					config = &initialConfig
				}
			}
			switch action {
			case "load":
				if namespace == "" || runtimeRef == "" || cwd == "" {
					return invalid("Namespace, runtime and cwd are required")
				}
				if err := resource.ValidateSessionID(nativeSessionID); err != nil {
					return invalid("Invalid native Session ID")
				}
				options, err := optionsInput.read(cmd)
				if err != nil {
					return err
				}
				v := resource.SessionLoad{Metadata: resource.Metadata{Namespace: namespace, Name: args[0]}, RuntimeRef: runtimeRef, Cwd: cwd, SessionID: nativeSessionID, Config: config, Features: options.Features, MCPServers: options.MCPServers}
				call = func(ctx context.Context, out any) error { return o.client.LoadSession(ctx, v, out) }
			case "cancel-load":
				if loadID == "" || !utf8.ValidString(loadID) {
					return invalid("Load ID is required and must be valid UTF-8")
				}
				call = func(ctx context.Context, out any) error { return o.client.CancelHistoryLoad(ctx, ref, loadID, out) }
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
				options, err := optionsInput.read(cmd)
				if err != nil {
					return err
				}
				v := resource.Session{Metadata: resource.Metadata{Namespace: namespace, Name: args[0]}, Spec: resource.SessionSpec{RuntimeRef: runtimeRef, SessionSpec: runtime.SessionSpec{Config: config, Cwd: cwd, SystemPrompt: systemPrompt, AdditionalDirs: dirs, Features: options.Features, MCPServers: options.MCPServers}}}

				if initial != nil {
					copyInput, err := runtime.CopyInput(*initial)
					if err != nil {
						return err
					}
					request := resource.SessionCreateInput{Metadata: v.Metadata, Spec: v.Spec, InitialInput: &copyInput}
					call = func(ctx context.Context, out any) error { return o.client.CreateSessionWithInput(ctx, request, out) }
				} else {
					call = func(ctx context.Context, out any) error { return o.client.CreateSession(ctx, v, out) }
				}
			case "list":
				call = func(ctx context.Context, out any) error {
					return o.client.ListSessions(ctx, resource.SessionQuery{Limit: limit, After: after}, out)
				}
			case "history":
				call = func(ctx context.Context, out any) error {
					return o.client.History(ctx, ref, store.HistoryQuery{Limit: limit, After: after}, out)
				}
			case "prompt":
				call = func(ctx context.Context, out any) error {
					return o.client.SendPrompt(ctx, ref, runtime.Input{Content: []events.ContentBlock{events.Text(text)}}, out)
				}
			case "get":
				call = func(ctx context.Context, out any) error { return o.client.GetSession(ctx, ref, out) }
			case "close":
				call = func(ctx context.Context, out any) error { return o.client.CloseSession(ctx, ref, out) }
			case "resume":
				if initial != nil {
					call = func(ctx context.Context, out any) error {
						return o.client.ResumeSessionWithInput(ctx, ref, *initial, out)
					}
				} else {
					call = func(ctx context.Context, out any) error { return o.client.ResumeSession(ctx, ref, out) }
				}
			case "cancel":
				call = func(ctx context.Context, out any) error { return o.client.Cancel(ctx, ref, out) }
			case "watch":
				return o.watch(cmd, ref)
			}
			op := action
			if op == "prompt" {
				op = "input"
			}
			return o.result(cmd, "session."+op, "", call)
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
		if err := o.printEvent(cmd.OutOrStdout(), raw); err != nil {
			return err
		}
	}
}
