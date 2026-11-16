package subcommand

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newObserveTriggersCommand() *cobra.Command {
	var configPath string
	filters := map[string]*string{}
	command := &cobra.Command{
		Use: "triggers [trigger-id]", Short: "Read all Trigger paths and fixed delivery evidence", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			query := url.Values{}
			resource := "triggers"
			for key, value := range filters {
				if *value != "" {
					query.Set(key, *value)
				}
			}
			if len(args) == 1 {
				resource += "/" + url.PathEscape(args[0])
				// Detail identity is Project + Trigger, never guessed from an instance list.
				query = url.Values{"project": {*filters["project"]}}
			} else {
				query.Set("limit", command.Flag("limit").Value.String())
			}
			var result json.RawMessage
			if err := client.Request(command.Context(), http.MethodGet, "/observe/"+resource+"?"+query.Encode(), nil, &result); err != nil {
				return err
			}
			printJSON(result)
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
	for _, flag := range []struct{ name, help string }{
		{"project", "Configured Project ID; required for detail, all configured Projects for list"},
		{"source", "Exact connector/account identity"}, {"thread", "Exact source-local Thread identity"},
		{"decision", "Exact durable decision: pending, ignored, associated, creating, created, unknown"},
		{"route", "Input path: router, direct, ignored or unknown"}, {"from", "Inclusive received time, RFC3339 with timezone"},
		{"to", "Inclusive received time, RFC3339 with timezone"}, {"search", "Search identity, kind and summary"},
		{"cursor", "Opaque frozen traversal cursor; expires after 15 minutes or server restart"},
	} {
		value := new(string)
		filters[flag.name] = value
		command.Flags().StringVar(value, flag.name, "", flag.help)
	}
	command.Flags().Int("limit", 50, "Page size, from 1 to 200")
	_ = command.MarkFlagRequired("config")
	return command
}
