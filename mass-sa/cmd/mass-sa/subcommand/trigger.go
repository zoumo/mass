package subcommand

import (
	"encoding/json"
	"net/url"

	"github.com/spf13/cobra"
	sa "github.com/zoumo/mass/mass-sa"
)

func newTriggerCommand() *cobra.Command {
	root := &cobra.Command{Use: "trigger", Short: "Persist source facts and inspect their ownership"}
	var configPath, content string
	var req sa.TriggerRequest
	accept := &cobra.Command{Use: "accept", RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		req.Content = json.RawMessage(content)
		var result sa.TriggerReceipt
		if err := client.Request(cmd.Context(), "POST", "/triggers", req, &result); err != nil {
			return err
		}
		printJSON(result)
		return nil
	}}
	get := &cobra.Command{Use: "get <trigger-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		var result sa.TriggerState
		if err := client.Request(cmd.Context(), "GET", "/triggers/"+url.PathEscape(args[0])+"?projectId="+url.QueryEscape(req.ProjectID), nil, &result); err != nil {
			return err
		}
		printJSON(result)
		return nil
	}}
	for _, cmd := range []*cobra.Command{accept, get} {
		cmd.Flags().StringVar(&configPath, "config", "", "Configuration path")
		cmd.Flags().StringVar(&req.ProjectID, "project", "mass", "Configured Project ID")
		_ = cmd.MarkFlagRequired("config")
	}
	accept.Flags().StringVar(&req.Source, "source", "", "Connector/account identity")
	accept.Flags().StringVar(&req.SourceEventID, "source-event-id", "", "Stable source event identity")
	accept.Flags().StringVar(&req.ThreadID, "thread", "", "Source-local Thread identity")
	accept.Flags().StringVar(&req.Kind, "kind", "message", "Normalized event kind")
	accept.Flags().StringVar(&req.Summary, "summary", "", "Normalized summary")
	accept.Flags().StringVar(&content, "content", "{}", "Normalized JSON content")
	accept.Flags().BoolVar(&req.ReplyRequested, "reply-requested", false, "Request communication through the Project Router")
	accept.Flags().StringVar(&req.EchoOfOutputID, "echo-of-output", "", "Stable output ID echoed by its authorized channel; persist without executing")
	for _, name := range []string{"source", "source-event-id", "thread", "summary"} {
		_ = accept.MarkFlagRequired(name)
	}
	root.AddCommand(accept, get)
	return root
}
