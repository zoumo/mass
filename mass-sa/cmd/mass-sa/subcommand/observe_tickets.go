package subcommand

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newObserveTicketsCommand() *cobra.Command {
	var configPath string
	filters := map[string]*string{}
	command := &cobra.Command{
		Use: "tickets [ticket-id]", Short: "Read Ticket ownership, delegation and fixed execution evidence", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			query := url.Values{}
			resource := "tickets"
			for key, value := range filters {
				if *value != "" {
					query.Set(key, *value)
				}
			}
			if len(args) == 1 {
				resource += "/" + url.PathEscape(args[0])
				// Detail identity is Project + Ticket, never guessed from an instance list.
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
		{"state", "Exact Ticket business state"},
		{"parent", "Exact parent Ticket identity"}, {"root", "Exact root Ticket identity"},
		{"search", "Search Ticket identity, title, objective, acceptance or Assignment identity"},
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
