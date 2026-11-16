package subcommand

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func addResourceObserveCommands(root *cobra.Command) {
	for _, resource := range []string{"sessions", "session", "runtimes", "runtime", "history"} {
		resource := resource
		var configPath, project, cursor, scope, namespace, runtime, state, kind, eventTypes, window string
		var limit int
		use := resource
		args := cobra.NoArgs
		if resource == "session" || resource == "history" {
			use += " <session-id>"
			args = cobra.ExactArgs(1)
		}
		if resource == "runtime" {
			use += " <runtime-name>"
			args = cobra.ExactArgs(1)
		}
		command := &cobra.Command{Use: use, Args: args, RunE: func(command *cobra.Command, args []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			query := url.Values{}
			for k, v := range map[string]string{"project": project, "cursor": cursor, "scope": scope, "namespace": namespace, "runtime": runtime, "state": state, "type": kind, "eventTypes": eventTypes, "window": window} {
				if v != "" {
					query.Set(k, v)
				}
			}
			if limit != 0 {
				query.Set("limit", command.Flag("limit").Value.String())
			}
			target := resource
			switch resource {
			case "session":
				target = "sessions/" + url.PathEscape(args[0])
			case "runtime":
				target = "runtimes/" + url.PathEscape(args[0])
			case "history":
				target = "sessions/" + url.PathEscape(args[0]) + "/history"
			}
			var result json.RawMessage
			if err := client.Request(command.Context(), http.MethodGet, "/observe/"+target+"?"+query.Encode(), nil, &result); err != nil {
				return err
			}
			printJSON(result)
			return nil
		}}
		command.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
		command.Flags().StringVar(&project, "project", "", "Configured Project context, or whole instance when omitted")
		command.Flags().StringVar(&scope, "scope", "associated", "associated or all massd resources")
		if resource == "sessions" || resource == "history" || resource == "runtimes" {
			command.Flags().StringVar(&cursor, "cursor", "", "Observer traversal cursor for lists, or native history cursor")
			command.Flags().IntVar(&limit, "limit", 50, "Page size from 1 to 200")
		}
		if resource == "sessions" || resource == "runtimes" {
			command.Flags().StringVar(&namespace, "namespace", "", "Namespace filter")
			command.Flags().StringVar(&state, "state", "", "Observed state filter")
		}
		if resource == "sessions" {
			command.Flags().StringVar(&runtime, "runtime", "", "Runtime filter")
		}
		if resource == "runtimes" {
			command.Flags().StringVar(&kind, "type", "", "Runtime type filter")
		}
		if resource == "history" {
			command.Flags().StringVar(&eventTypes, "event-types", "", "Comma-separated durable SessionEvent types")
			command.Flags().StringVar(&window, "window", "recent", "recent tail scan or page with native cursors")
		}
		_ = command.MarkFlagRequired("config")
		root.AddCommand(command)
	}
}
