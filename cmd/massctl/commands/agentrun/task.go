package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newTaskCmd(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage agent tasks",
	}
	cmd.AddCommand(newTaskDoCmd(getClient))
	cmd.AddCommand(newTaskDoneCmd())
	cmd.AddCommand(newTaskGetCmd(getClient))
	cmd.AddCommand(newTaskRetryCmd(getClient))
	return cmd
}

func newTaskDoneCmd() *cobra.Command {
	var (
		filePath string
		reason   string
		response string
	)

	cmd := &cobra.Command{
		Use:   "done",
		Short: "Mark a task as done by writing response to the task file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var rawResp json.RawMessage
			if err := json.Unmarshal([]byte(response), &rawResp); err != nil {
				return fmt.Errorf("--response is not valid JSON: %w", err)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(rawResp, &obj); err != nil {
				return fmt.Errorf("--response must be a JSON object: %w", err)
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("read task file: %w", err)
			}
			var task pkgariapi.AgentTask
			if err := json.Unmarshal(data, &task); err != nil {
				return fmt.Errorf("parse task file: %w", err)
			}

			now := time.Now()
			task.Done = true
			task.Reason = reason
			task.UpdatedAt = &now
			task.Response = rawResp

			tmp := filePath + ".tmp"
			out, err := json.MarshalIndent(task, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal task: %w", err)
			}
			if err := os.WriteFile(tmp, out, 0o644); err != nil {
				return fmt.Errorf("write temp file: %w", err)
			}
			if err := os.Rename(tmp, filePath); err != nil {
				return fmt.Errorf("rename temp file: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Task %s marked as done.\n", task.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Path to the task JSON file (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "Outcome summary, e.g. success, failed, needs_human (required)")
	cmd.Flags().StringVar(&response, "response", "", "Result as JSON object (required)")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("reason")
	_ = cmd.MarkFlagRequired("response")
	return cmd
}

func newTaskDoCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws          string
		run         string
		description string
		filePaths   []string
	)

	cmd := &cobra.Command{
		Use:   "do",
		Short: "Dispatch a task to an agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().TaskDo(context.Background(), &pkgariapi.AgentRunTaskDoParams{
				Workspace:   ws,
				Name:        run,
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
	cmd.Flags().StringVar(&run, "run", "", "Agent run name (required)")
	cmd.Flags().StringVar(&description, "description", "", "Task description (required)")
	cmd.Flags().StringSliceVar(&filePaths, "files", nil, "Input file paths")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("run")
	_ = cmd.MarkFlagRequired("description")
	return cmd
}

// newTaskGetCmd handles both single-task lookup (with positional IDs) and list-all.
// Usage:
//
//	task get -w ws --run agent              → list all tasks
//	task get -w ws --run agent id1 id2      → get specific tasks
func newTaskGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws     string
		run    string
		format cliutil.OutputFormat
	)

	cmd := &cobra.Command{
		Use:   "get [task-id...]",
		Short: "Get or list tasks for an agent run",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			printer := &cliutil.ResourcePrinter{Format: format, Columns: taskColumns()}

			if len(args) == 0 {
				result, err := client.AgentRuns().TaskList(ctx, &pkgariapi.AgentRunTaskListParams{
					Workspace: ws,
					Name:      run,
				})
				if err != nil {
					return err
				}
				items := make([]any, len(result.Items))
				for i := range result.Items {
					items[i] = result.Items[i]
				}
				return printer.PrintList(cmd.OutOrStdout(), items, result)
			}

			var tasks []any
			for _, id := range args {
				task, err := client.AgentRuns().TaskGet(ctx, &pkgariapi.AgentRunTaskGetParams{
					Workspace: ws,
					Name:      run,
					TaskID:    id,
				})
				if err != nil {
					return fmt.Errorf("task %s: %w", id, err)
				}
				tasks = append(tasks, *task)
			}
			return printer.Print(cmd.OutOrStdout(), tasks)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&run, "run", "", "Agent run name (required)")
	cliutil.AddOutputFlag(cmd, &format)
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("run")
	return cmd
}

func newTaskRetryCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws  string
		run string
	)

	cmd := &cobra.Command{
		Use:   "retry <task-id>",
		Short: "Retry an existing agent task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().TaskRetry(context.Background(), &pkgariapi.AgentRunTaskRetryParams{
				Workspace: ws,
				Name:      run,
				TaskID:    args[0],
			})
			if err != nil {
				return err
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&run, "run", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("run")
	return cmd
}

func taskColumns() []cliutil.Column {
	return []cliutil.Column{
		{Header: "ID", Field: func(v any) string { return v.(pkgariapi.AgentTask).ID }},
		{Header: "ASSIGNEE", Field: func(v any) string { return v.(pkgariapi.AgentTask).Assignee }},
		{Header: "ATTEMPT", Field: func(v any) string { return fmt.Sprintf("%d", v.(pkgariapi.AgentTask).Attempt) }},
		{Header: "DONE", Field: func(v any) string {
			if v.(pkgariapi.AgentTask).Done {
				return "true"
			}
			return "false"
		}},
		{Header: "REASON", Field: func(v any) string {
			task := v.(pkgariapi.AgentTask)
			if task.Done {
				return task.Reason
			}
			return "pending"
		}},
		{Header: "AGE", Field: func(v any) string { return cliutil.FormatAge(v.(pkgariapi.AgentTask).CreatedAt) }},
	}
}
