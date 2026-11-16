package subcommand

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newTicketCommand() *cobra.Command {
	root := &cobra.Command{Use: "ticket", Short: "Create and inspect independent work"}
	var configPath, projectID, title, objective, acceptance string
	create := &cobra.Command{Use: "create", RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		var result any
		err = client.Request(cmd.Context(), "POST", "/tickets", map[string]string{"projectId": projectID, "title": title, "objective": objective, "acceptance": acceptance}, &result)
		if err == nil {
			printJSON(result)
		}
		return err
	}}
	get := &cobra.Command{Use: "get <ticket-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		var result any
		err = client.Request(cmd.Context(), "GET", "/tickets/"+url.PathEscape(args[0])+"?projectId="+url.QueryEscape(projectID), nil, &result)
		if err == nil {
			printJSON(result)
		}
		return err
	}}
	inspect := &cobra.Command{Use: "inspect <root-ticket-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		result, err := client.InspectTicket(cmd.Context(), projectID, args[0])
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	}}
	for _, cmd := range []*cobra.Command{create, get, inspect} {
		cmd.Flags().StringVar(&configPath, "config", "", "Configuration path")
		cmd.Flags().StringVar(&projectID, "project", "mass", "Configured project ID")
		_ = cmd.MarkFlagRequired("config")
	}
	create.Flags().StringVar(&title, "title", "", "Ticket title")
	create.Flags().StringVar(&objective, "objective", "", "Requested objective")
	create.Flags().StringVar(&acceptance, "acceptance", "", "Acceptance criteria")
	_ = create.MarkFlagRequired("objective")
	root.AddCommand(create, get, inspect)
	return root
}
