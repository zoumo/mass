// Package agent provides agent definition management commands.
package agent

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/zoumo/oar/api"
	"github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
)

// NewCommand returns the "agent" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent definition management commands",
	}
	cmd.AddCommand(newApplyCmd(getClient))
	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(newListCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	return cmd
}

func newApplyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply (create or update) an agent definition from a YAML file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading agent file %q: %w", file, err)
			}
			var ag ari.Agent
			if err := yaml.Unmarshal(data, &ag); err != nil {
				return fmt.Errorf("parsing agent YAML %q: %w", file, err)
			}
			if ag.Metadata.Name == "" {
				return fmt.Errorf("agent YAML must have a non-empty 'metadata.name' field")
			}
			if ag.Spec.Command == "" {
				return fmt.Errorf("agent YAML must have a non-empty 'spec.command' field")
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.AgentSetParams{
				Name:                  ag.Metadata.Name,
				Command:               ag.Spec.Command,
				Args:                  ag.Spec.Args,
				Env:                   ag.Spec.Env,
				StartupTimeoutSeconds: ag.Spec.StartupTimeoutSeconds,
			}
			var result ari.AgentSetResult
			if err := client.Call(api.MethodAgentSet, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to agent YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get an agent definition by name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.AgentGetParams{Name: name}
			var result ari.AgentGetResult
			if err := client.Call(api.MethodAgentGet, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newListCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all agent definitions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var result ari.AgentListResult
			if err := client.Call(api.MethodAgentList, ari.AgentListParams{}, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an agent definition by name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Call(api.MethodAgentDelete, ari.AgentDeleteParams{Name: name}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent %q deleted\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
