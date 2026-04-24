package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newTaskCmd(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage agent tasks",
	}
	cmd.AddCommand(newTaskCreateCmd(getClient))
	cmd.AddCommand(newTaskRetryCmd(getClient))
	cmd.AddCommand(newTaskGetCmd(getClient))
	cmd.AddCommand(newTaskListCmd(getClient))
	return cmd
}

func newTaskCreateCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws          string
		name        string
		description string
		filePaths   []string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a task for an agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().TaskCreate(context.Background(), &pkgariapi.AgentRunTaskCreateParams{
				Workspace:   ws,
				Name:        name,
				Description: description,
				FilePaths:   filePaths,
			})
			if err != nil {
				return err
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	cmd.Flags().StringVar(&description, "description", "", "Task description (required)")
	cmd.Flags().StringSliceVar(&filePaths, "file", nil, "Input file paths")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("description")
	return cmd
}

func newTaskGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws     string
		name   string
		taskID string
		format cliutil.OutputFormat
	)

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get an agent task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			task, err := client.AgentRuns().TaskGet(context.Background(), &pkgariapi.AgentRunTaskGetParams{
				Workspace: ws,
				Name:      name,
				TaskID:    taskID,
			})
			if err != nil {
				return err
			}

			printer := &cliutil.ResourcePrinter{Format: format, Columns: taskColumns()}
			return printer.Print(cmd.OutOrStdout(), []any{*task})
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	cmd.Flags().StringVar(&taskID, "id", "", "Task ID (required)")
	cliutil.AddOutputFlag(cmd, &format)
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newTaskRetryCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws     string
		name   string
		taskID string
	)

	cmd := &cobra.Command{
		Use:   "retry",
		Short: "Retry an existing agent task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().TaskRetry(context.Background(), &pkgariapi.AgentRunTaskRetryParams{
				Workspace: ws,
				Name:      name,
				TaskID:    taskID,
			})
			if err != nil {
				return err
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	cmd.Flags().StringVar(&taskID, "id", "", "Task ID (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newTaskListCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws     string
		name   string
		format cliutil.OutputFormat
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks for an agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().TaskList(context.Background(), &pkgariapi.AgentRunTaskListParams{
				Workspace: ws,
				Name:      name,
			})
			if err != nil {
				return err
			}

			printer := &cliutil.ResourcePrinter{Format: format, Columns: taskColumns()}
			items := make([]any, len(result.Items))
			for i := range result.Items {
				items[i] = result.Items[i]
			}
			return printer.PrintList(cmd.OutOrStdout(), items, result)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name (required)")
	cliutil.AddOutputFlag(cmd, &format)
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func taskColumns() []cliutil.Column {
	return []cliutil.Column{
		{Header: "ID", Field: func(v any) string { return v.(pkgariapi.AgentTask).ID }},
		{Header: "ASSIGNEE", Field: func(v any) string { return v.(pkgariapi.AgentTask).Assignee }},
		{Header: "ATTEMPT", Field: func(v any) string { return fmt.Sprintf("%d", v.(pkgariapi.AgentTask).Attempt) }},
		{Header: "COMPLETED", Field: func(v any) string {
			if v.(pkgariapi.AgentTask).Completed {
				return "true"
			}
			return "false"
		}},
		{Header: "STATUS", Field: func(v any) string {
			task := v.(pkgariapi.AgentTask)
			if !task.Completed {
				return "pending"
			}
			if task.Response == nil {
				return "completed"
			}
			return task.Response.Status
		}},
		{Header: "AGE", Field: func(v any) string { return cliutil.FormatAge(v.(pkgariapi.AgentTask).CreatedAt) }},
		{Header: "DESCRIPTION", Field: func(v any) string {
			task := v.(pkgariapi.AgentTask)
			if task.Completed && task.Response != nil && task.Response.Description != "" {
				return task.Response.Description
			}
			return task.Request.Description
		}, Wide: true},
	}
}
