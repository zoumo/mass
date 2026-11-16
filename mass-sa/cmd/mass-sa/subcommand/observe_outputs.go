package subcommand

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newObserveOutputsCommand() *cobra.Command {
	var configPath string
	filters := map[string]*string{}
	command := &cobra.Command{
		Use: "outputs [output-id]", Short: "Inspect immutable outputs and consumer audit without consuming them", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			query := url.Values{}
			resource := "outputs"
			for key, value := range filters {
				if *value != "" {
					query.Set(key, *value)
				}
			}
			if len(args) == 1 {
				resource += "/" + url.PathEscape(args[0])
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
	for _, flag := range []struct{ name, key, help string }{
		{"project", "project", "Configured Project ID; required for detail"},
		{"trigger", "trigger", "Exact source Trigger ID (never matches another Trigger in its Thread)"},
		{"ticket", "ticket", "Exact Ticket ID"},
		{"source", "source", "Exact target connector/account identity"},
		{"thread", "thread", "Exact target Thread identity"},
		{"category", "category", "reply, fact or other; reply contains only explicit reply kinds"},
		{"related-trigger", "relatedTrigger", "Associated Ticket facts explicitly targeting this Trigger's Thread; requires project and category=fact"},
		{"cursor", "cursor", "Opaque frozen traversal cursor; expires after 15 minutes or server restart"},
	} {
		value := new(string)
		filters[flag.key] = value
		command.Flags().StringVar(value, flag.name, "", flag.help)
	}
	command.Flags().Int("limit", 50, "Page size, from 1 to 200")
	_ = command.MarkFlagRequired("config")
	return command
}
