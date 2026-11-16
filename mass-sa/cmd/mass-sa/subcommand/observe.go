package subcommand

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newObserveCommand() *cobra.Command {
	root := &cobra.Command{Use: "observe", Short: "Read the Observer projection without consuming outputs or changing work"}
	for _, resource := range []string{"health", "projects", "routers"} {
		resource := resource
		var configPath, project, cursor string
		var limit int
		command := &cobra.Command{
			Use: resource, Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				client, err := managementClient(configPath)
				if err != nil {
					return err
				}
				query := url.Values{}
				if project != "" {
					query.Set("project", project)
				}
				if cursor != "" {
					query.Set("cursor", cursor)
				}
				if limit != 0 {
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
		command.Flags().StringVar(&project, "project", "", "Configured Project ID (all by default)")
		command.Flags().StringVar(&cursor, "cursor", "", "Opaque cursor returned by the previous page")
		command.Flags().IntVar(&limit, "limit", 50, "Page size, from 1 to 200")
		_ = command.MarkFlagRequired("config")
		root.AddCommand(command)
	}
	root.AddCommand(newObserveTriggersCommand())
	addResourceObserveCommands(root)
	root.AddCommand(newObserveOutputsCommand())
	root.AddCommand(newObserveTicketsCommand())
	return root
}
