package subcommand

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/superagent/triggers"
)

func newTriggers() *cobra.Command {
	command := &cobra.Command{Use: "triggers", Short: "Run and inspect Superagent triggers"}
	for _, kind := range []string{triggers.GitHub, triggers.DWS} {
		trigger := &cobra.Command{Use: kind}
		trigger.AddCommand(newTriggerConfigCommand(kind, "check"), newTriggerConfigCommand(kind, "serve"))
		var endpoint string
		status := &cobra.Command{
			Use:   "status",
			Short: "Read the local trigger status",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return triggers.Status(cmd.Context(), kind, endpoint, cmd.OutOrStdout())
			},
		}
		status.Flags().StringVar(&endpoint, "endpoint", "", "Local trigger status origin")
		_ = status.MarkFlagRequired("endpoint")
		trigger.AddCommand(status)
		command.AddCommand(trigger)
	}
	return command
}

func newTriggerConfigCommand(kind, action string) *cobra.Command {
	var path, coreEndpoint string
	description := "Run the trigger in the foreground"
	if action == "check" {
		description = "Validate configuration and local prerequisites"
	}
	command := &cobra.Command{
		Use:   action,
		Short: description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			var section io.Reader
			if cmd.Flags().Changed("compose-core-endpoint") {
				section = cmd.InOrStdin()
			}
			component, err := triggers.Load(kind, path, section, coreEndpoint)
			if err != nil {
				return err
			}
			if action == "check" {
				return component.Check(ctx, cmd.OutOrStdout())
			}
			return component.Serve(ctx, cmd.OutOrStdout())
		},
	}
	command.Flags().StringVar(&path, "config", "", "Trigger YAML configuration file")
	_ = command.MarkFlagRequired("config")
	command.Flags().StringVar(&coreEndpoint, "compose-core-endpoint", "", "Use the Compose configuration section and Core endpoint")
	_ = command.Flags().MarkHidden("compose-core-endpoint")
	return command
}
