package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

const maxIdleRetries = 2

type waitStatus struct {
	Elapsed string `json:"elapsed"`
	Agent   string `json:"agent"`
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Reason  string `json:"reason,omitempty"`
	Retry   string `json:"retry,omitempty"`
	Error   string `json:"error,omitempty"`
}

func newTaskWaitCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws       string
		run      string
		timeout  time.Duration
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "wait <task-id>",
		Short: "Poll until a task completes or an error occurs",
		Long: `Poll agent run phase and task status until one of:
  - task done → print task JSON to stdout (exit 0)
  - agent error/stopped → print error to stderr (exit 2)
  - agent idle + task not done after retries → print error to stderr (exit 1)
  - timeout → print error to stderr (exit 3)

Progress is reported as JSONL on stderr. Task result JSON is on stdout.`,
		Example: `  massctl ar task wait t-abc123 -w my-ws --run worker
  massctl ar task wait t-abc123 -w my-ws --run worker --timeout 5m --interval 5s`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			taskID := args[0]
			ctx := context.Background()
			stderr := cmd.ErrOrStderr()
			stdout := cmd.OutOrStdout()

			idleRetryCount := 0
			start := time.Now()

			for {
				elapsed := time.Since(start).Truncate(time.Second)

				var ar pkgariapi.AgentRun
				if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: ws, Name: run}, &ar); err != nil {
					return fmt.Errorf("agentrun/get %q: %w", run, err)
				}
				agentPhase := string(ar.Status.Phase)

				task, err := client.AgentRuns().TaskGet(ctx, &pkgariapi.AgentRunTaskGetParams{
					Workspace: ws,
					Name:      run,
					TaskID:    taskID,
				})
				if err != nil {
					return fmt.Errorf("task/get %q: %w", taskID, err)
				}

				taskStatus := "pending"
				if task.Done {
					taskStatus = "done"
				}

				if task.Done {
					writeStatus(stderr, waitStatus{
						Elapsed: elapsed.String(),
						Agent:   agentPhase,
						Task:    taskStatus,
						Attempt: task.Attempt,
						Reason:  task.Reason,
					})
					return cliutil.PrintJSON(stdout, task)
				}

				if agentPhase == "error" || agentPhase == "stopped" {
					errMsg := fmt.Sprintf("agent %q entered %s state", run, agentPhase)
					if ar.Status.ErrorMessage != "" {
						errMsg += ": " + ar.Status.ErrorMessage
					}
					writeStatus(stderr, waitStatus{
						Elapsed: elapsed.String(),
						Agent:   agentPhase,
						Task:    taskStatus,
						Attempt: task.Attempt,
						Error:   errMsg,
					})
					os.Exit(2)
				}

				if agentPhase == "idle" {
					if idleRetryCount < maxIdleRetries {
						idleRetryCount++
						writeStatus(stderr, waitStatus{
							Elapsed: elapsed.String(),
							Agent:   agentPhase,
							Task:    taskStatus,
							Attempt: task.Attempt,
							Retry:   fmt.Sprintf("%d/%d", idleRetryCount, maxIdleRetries),
						})
						_, _ = client.AgentRuns().TaskRetry(ctx, &pkgariapi.AgentRunTaskRetryParams{
							Workspace: ws,
							Name:      run,
							TaskID:    taskID,
						})
					} else {
						errMsg := fmt.Sprintf("agent %q idle, task %q not completed after %d retries", run, taskID, maxIdleRetries)
						writeStatus(stderr, waitStatus{
							Elapsed: elapsed.String(),
							Agent:   agentPhase,
							Task:    taskStatus,
							Attempt: task.Attempt,
							Error:   errMsg,
						})
						os.Exit(1)
					}
				} else {
					writeStatus(stderr, waitStatus{
						Elapsed: elapsed.String(),
						Agent:   agentPhase,
						Task:    taskStatus,
						Attempt: task.Attempt,
					})
				}

				if elapsed >= timeout {
					errMsg := fmt.Sprintf("timeout after %s waiting for task %q", timeout, taskID)
					writeStatus(stderr, waitStatus{
						Elapsed: elapsed.String(),
						Agent:   agentPhase,
						Task:    taskStatus,
						Attempt: task.Attempt,
						Error:   errMsg,
					})
					os.Exit(3)
				}

				time.Sleep(interval)
			}
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&run, "run", "", "Agent run name (required)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "Max wait duration")
	cmd.Flags().DurationVar(&interval, "interval", 10*time.Second, "Poll interval")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("run")
	return cmd
}

func writeStatus(w io.Writer, s waitStatus) {
	data, _ := json.Marshal(s)
	fmt.Fprintln(w, string(data))
}
