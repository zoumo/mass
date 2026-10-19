package commands

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zoumo/mass/mass-go/internal/client"
)

func (o *options) session() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Aliases: []string{"s"}, Short: "Create, inspect and control sessions", Args: cobra.NoArgs, RunE: showHelp}
	cmd.AddCommand(o.unary("list", "List sessions", "agentd/session/list", 0, nil))
	status := o.unary("status SESSION_NAME", "Get session status", "agentd/session/status", 1, selector)
	status.Aliases = []string{"get"}
	cmd.AddCommand(status)
	var runtimeName, cwd, initialInput string
	create := o.unary("new SESSION_NAME", "Create a session; optionally send explicit initial input to the Go server", "agentd/session/new", 1, func(cmd *cobra.Command, args []string) (any, error) {
		if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(runtimeName) == "" {
			return nil, errors.New("session name and --runtime-name must not be empty")
		}
		if !filepath.IsAbs(cwd) {
			return nil, errors.New("--cwd must be an absolute path in the runtime filesystem")
		}
		params := map[string]any{"sessionName": args[0], "spec": map[string]string{"runtimeName": runtimeName, "cwd": cwd}}
		if cmd.Flags().Changed("prompt") {
			params["initialInput"] = initialInput
		}
		return params, nil
	})
	create.Aliases = []string{"create"}
	create.Flags().StringVar(&runtimeName, "runtime-name", "", "configured runtime name (required)")
	create.Flags().StringVar(&cwd, "cwd", "", "absolute working directory on the runtime host (required)")
	create.Flags().StringVar(&initialInput, "prompt", "", "explicit initial input (Go server only; required to open a Claude session)")
	_ = create.MarkFlagRequired("runtime-name")
	_ = create.MarkFlagRequired("cwd")
	cmd.AddCommand(create)
	for _, action := range []struct{ name, description string }{
		{"archive", "Release the session attachment; preserve identity and history"},
		{"resume", "Resume an offline session"},
		{"cancel", "Interrupt current work"},
	} {
		c := o.unary(action.name+" SESSION_NAME", action.description, "agentd/session/"+action.name, 1, selector)
		if action.name == "archive" {
			c.Aliases = []string{"delete"}
			c.Long = "Archive the attachment and preserve all history. The delete alias is not a physical deletion."
		}
		cmd.AddCommand(c)
	}
	cmd.AddCommand(o.unary("prompt SESSION_NAME TEXT", "Submit one text prompt; return native acceptance receipt", "agentd/session/prompt", 2, func(_ *cobra.Command, args []string) (any, error) {
		if strings.TrimSpace(args[1]) == "" {
			return nil, errors.New("prompt must not be empty")
		}
		return map[string]any{"sessionName": args[0], "prompt": []any{map[string]string{"type": "text", "text": args[1]}}}, nil
	}))
	var after uint64
	var limit int
	var page bool
	events := o.unary("get-event SESSION_NAME", "Read one page of historical events as JSONL", "agentd/session/get_event", 1, func(_ *cobra.Command, args []string) (any, error) {
		if limit < 1 || limit > 1000 {
			return nil, errors.New("--limit must be 1..1000")
		}
		return map[string]any{"sessionName": args[0], "afterSeq": after, "limit": limit}, nil
	})
	events.Aliases = []string{"events", "get-events", "get_event"}
	events.Flags().Uint64Var(&after, "after-seq", 0, "exclusive history cursor")
	events.Flags().IntVar(&limit, "limit", 100, "maximum events in the page (1..1000)")
	events.Flags().BoolVar(&page, "page", false, "emit the full JSON page including nextSeq")
	cmd.AddCommand(events)
	watch := &cobra.Command{Use: "watch SESSION_NAME", Aliases: []string{"watch-event", "watch-events", "watch_event"}, Short: "Watch live events as JSONL; no replay or reconnect", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.New(o.endpoint, o.timeout)
		if err != nil {
			return err
		}
		defer c.Close()
		return c.Watch(cmd.Context(), args[0], func(event json.RawMessage) error { return json.NewEncoder(cmd.OutOrStdout()).Encode(event) })
	}}
	cmd.AddCommand(watch)
	return cmd
}
