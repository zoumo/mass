package subcommand

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	sa "github.com/zoumo/mass/mass-sa"
)

// NewRootCommand builds the mass-sa Cobra command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "mass-sa",
		Short:         "Project-level Super Agent demo",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(os.Stderr)
	root.SetErr(os.Stderr)
	root.AddCommand(
		newServeCommand(),
		newTicketCommand(),
		newTriggerCommand(),
		newOutputCommand(),
		newEventCommand(),
		newWorkerCommand(),
		newRouteCommand(),
		newMCPCommand(),
	)
	return root
}

func newServeCommand() *cobra.Command {
	var configPath string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Start the management API, tools, routers, and dispatcher",
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := sa.LoadConfig(configPath)
			if err != nil {
				return err
			}
			return sa.RunServe(command.Context(), config, sa.WithServeReady(func(ready sa.ServerReady) {
				printJSON(ready)
			}))
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
	_ = command.MarkFlagRequired("config")
	return command
}

func newEventCommand() *cobra.Command {
	var configPath, projectID, rootTicketID, routeKey, source, kind, summary, payload string
	command := &cobra.Command{
		Use:   "event",
		Short: "Accept one external event",
		RunE: func(command *cobra.Command, _ []string) error {
			if !json.Valid([]byte(payload)) {
				return fmt.Errorf("payload must be valid JSON")
			}
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			result, err := client.SubmitEvent(command.Context(), sa.AcceptEventRequest{
				ProjectID:    projectID,
				RootTicketID: rootTicketID,
				RouteKey:     routeKey,
				Source:       source,
				Kind:         kind,
				Summary:      summary,
				Payload:      json.RawMessage(payload),
			})
			if err != nil {
				return err
			}
			printJSON(result)
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
	command.Flags().StringVar(&projectID, "project", "mass", "Configured project ID")
	command.Flags().StringVar(&rootTicketID, "root-ticket", "", "Root Ticket ID")
	command.Flags().StringVar(&routeKey, "route-key", "", "Optional stable route key")
	command.Flags().StringVar(&source, "source", "cli", "Event source label")
	command.Flags().StringVar(&kind, "kind", "", "Event kind")
	command.Flags().StringVar(&summary, "summary", "", "Human-readable event summary")
	command.Flags().StringVar(&payload, "payload", "{}", "JSON object payload")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("root-ticket")
	_ = command.MarkFlagRequired("kind")
	_ = command.MarkFlagRequired("summary")
	return command
}

func newWorkerCommand() *cobra.Command {
	command := &cobra.Command{Use: "worker", Short: "Inspect workers"}
	var configPath, projectID, rootTicketID string
	list := &cobra.Command{
		Use:   "list",
		Short: "List workers in one root Ticket",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			result, err := client.ListWorkers(command.Context(), projectID, rootTicketID)
			if err != nil {
				return err
			}
			printJSON(result)
			return nil
		},
	}
	list.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
	list.Flags().StringVar(&projectID, "project", "mass", "Configured project ID")
	list.Flags().StringVar(&rootTicketID, "root-ticket", "", "Root Ticket ID")
	_ = list.MarkFlagRequired("config")
	_ = list.MarkFlagRequired("root-ticket")
	command.AddCommand(list)
	return command
}

func newRouteCommand() *cobra.Command {
	var configPath, projectID, rootTicketID, routeKey, workerName, assignmentID string
	command := &cobra.Command{Use: "route", Short: "Manage event route bindings"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List current route bindings",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			result, err := client.ListRoutes(command.Context(), projectID, rootTicketID)
			if err != nil {
				return err
			}
			printJSON(map[string]any{"routeBindings": result})
			return nil
		},
	}
	bind := &cobra.Command{
		Use:   "bind",
		Short: "Bind a route key to a worker Assignment",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			result, err := client.BindRoute(command.Context(), sa.BindEventRouteRequest{
				ProjectID:    projectID,
				RootTicketID: rootTicketID,
				RouteKey:     routeKey,
				WorkerName:   workerName,
				AssignmentID: assignmentID,
			})
			if err != nil {
				return err
			}
			printJSON(result)
			return nil
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a route key and read back the removed binding",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := managementClient(configPath)
			if err != nil {
				return err
			}
			result, err := client.RevokeRoute(command.Context(), sa.BindEventRouteRequest{
				ProjectID:    projectID,
				RootTicketID: rootTicketID,
				RouteKey:     routeKey,
			})
			if err != nil {
				return err
			}
			printJSON(result)
			return nil
		},
	}
	for _, child := range []*cobra.Command{list, bind, revoke} {
		child.Flags().StringVar(&configPath, "config", "", "Viper YAML configuration path")
		child.Flags().StringVar(&projectID, "project", "mass", "Configured project ID")
		child.Flags().StringVar(&rootTicketID, "root-ticket", "", "Root Ticket ID")
		_ = child.MarkFlagRequired("config")
		_ = child.MarkFlagRequired("root-ticket")
	}
	bind.Flags().StringVar(&routeKey, "route-key", "", "Stable route key")
	bind.Flags().StringVar(&workerName, "worker", "", "Worker name")
	bind.Flags().StringVar(&assignmentID, "assignment", "", "Current worker Assignment ID")
	_ = bind.MarkFlagRequired("route-key")
	_ = bind.MarkFlagRequired("worker")
	_ = bind.MarkFlagRequired("assignment")
	revoke.Flags().StringVar(&routeKey, "route-key", "", "Stable route key")
	_ = revoke.MarkFlagRequired("route-key")
	command.AddCommand(list, bind, revoke)
	return command
}

func newMCPCommand() *cobra.Command {
	var projectID, role, capabilityPath string
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the capability-scoped MCP stdio surface",
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(role) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(capabilityPath) == "" {
				return fmt.Errorf("project, role, and capability are required")
			}
			return sa.RunMCPStdio(command.Context(), os.Stdin, os.Stdout, projectID, role, capabilityPath)
		},
	}
	command.Flags().StringVar(&projectID, "project", "", "Configured project ID")
	command.Flags().StringVar(&role, "role", "", "router or worker")
	command.Flags().StringVar(&capabilityPath, "capability", "", "Absolute 0600 capability file")
	_ = command.MarkFlagRequired("project")
	_ = command.MarkFlagRequired("role")
	_ = command.MarkFlagRequired("capability")
	return command
}

func managementClient(configPath string) (*sa.ManagementClient, error) {
	config, err := sa.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	return sa.NewManagementClient(config)
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, "mass-sa: encode output:", err)
	}
}
