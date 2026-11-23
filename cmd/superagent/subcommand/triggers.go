package subcommand

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/superagent/triggers"
	"github.com/zoumo/mass/superagent/triggers/dws"
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
		if kind == triggers.DWS {
			trigger.AddCommand(dwsEvidenceCommands()...)
		}
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

func dwsEvidenceCommands() []*cobra.Command {
	build := func(use, short string, args cobra.PositionalArgs, op func(dws.Component, *cobra.Command, []string) error) *cobra.Command {
		var path string
		c := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			adapter, err := dws.Load(path, nil, "")
			if err != nil {
				return err
			}
			return op(adapter, cmd, args)
		}}
		c.Flags().StringVar(&path, "config", "", "DWS Trigger YAML configuration file")
		_ = c.MarkFlagRequired("config")
		return c
	}
	var reason string
	release := build("release-input <source-message-id>", "Release a held DWS input with the adapter stopped", cobra.ExactArgs(1),
		func(c dws.Component, _ *cobra.Command, args []string) error { return c.ReleaseInput(args[0], reason) })
	release.Flags().StringVar(&reason, "reason", "", "Reason for releasing the verified input")
	_ = release.MarkFlagRequired("reason")
	var dismissReason string
	dismiss := build("dismiss-send <attempt-id>", "Dismiss an unknown DWS send after verifying no delivery", cobra.ExactArgs(1),
		func(c dws.Component, _ *cobra.Command, args []string) error {
			return c.DismissSend(args[0], dismissReason)
		})
	dismiss.Flags().StringVar(&dismissReason, "reason", "", "Reason the attempt is known to have no delivery")
	_ = dismiss.MarkFlagRequired("reason")
	return []*cobra.Command{
		build("inspect-evidence", "Inspect DWS input and send evidence with the adapter stopped", cobra.NoArgs,
			func(c dws.Component, cmd *cobra.Command, _ []string) error {
				return c.InspectEvidence(cmd.OutOrStdout())
			}),
		release,
		dismiss,
		build("import-sent-evidence", "Import legacy DWS send evidence from NDJSON on stdin with the adapter stopped", cobra.NoArgs,
			func(c dws.Component, cmd *cobra.Command, _ []string) error {
				return c.ImportSentEvidence(cmd.InOrStdin())
			}),
	}
}
