// Package agent provides agent template management commands.
package agent

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// applySpec is the YAML shape for agent apply -f.
type applySpec struct {
	Name                  string        `yaml:"name"`
	Command               string        `yaml:"command"`
	Args                  []string      `yaml:"args,omitempty"`
	Env                   []spec.EnvVar `yaml:"env,omitempty"`
	StartupTimeoutSeconds *int          `yaml:"startupTimeoutSeconds,omitempty"`
}

// NewCommand returns the "agent" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent template management commands",
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
		Short: "Apply (create or update) an agent template from a YAML file",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading agent-template file %q: %w", file, err)
			}
			var s applySpec
			if err := yaml.Unmarshal(data, &s); err != nil {
				return fmt.Errorf("parsing agent-template YAML %q: %w", file, err)
			}
			if s.Name == "" {
				return fmt.Errorf("agent-template YAML must have a non-empty 'name' field")
			}
			if s.Command == "" {
				return fmt.Errorf("agent-template YAML must have a non-empty 'command' field")
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.AgentTemplateSetParams{
				Name:                  s.Name,
				Command:               s.Command,
				Args:                  s.Args,
				Env:                   s.Env,
				StartupTimeoutSeconds: s.StartupTimeoutSeconds,
			}
			var result ari.AgentTemplateInfo
			if err := client.Call("agent/set", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to agent-template YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Get an agent template by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.AgentTemplateGetParams{Name: args[0]}
			var result ari.AgentTemplateGetResult
			if err := client.Call("agent/get", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
}

func newListCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all agent templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var result ari.AgentTemplateListResult
			if err := client.Call("agent/list", ari.AgentTemplateListParams{}, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an agent template by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Call("agent/delete", ari.AgentTemplateDeleteParams{Name: args[0]}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent template %q deleted\n", args[0])
			return nil
		},
	}
}
