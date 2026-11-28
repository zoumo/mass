package subcommand

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func newScheduledTasks() *cobra.Command {
	const collection = "/v1/core/scheduled-tasks"
	var endpoint, tokenFile, operationID, topic string
	var limit int
	var cursor string
	var newest bool
	cmd := &cobra.Command{Use: "scheduled-tasks", Short: "Read and trigger Core scheduled tasks"}
	cmd.PersistentFlags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:24370", "Core HTTP endpoint")
	cmd.PersistentFlags().StringVar(&tokenFile, "operator-token-file", "", "Private Core operator token file")
	_ = cmd.MarkPersistentFlagRequired("operator-token-file")
	list := &cobra.Command{
		Use: "list", Short: "List scheduled tasks", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			q := url.Values{}
			if topic != "" {
				q.Set("ownerType", "topic")
				q.Set("ownerId", topic)
			}
			return operatorRequest(command.Context(), command.OutOrStdout(), endpoint, tokenFile, http.MethodGet, collection+encodeRecordPage(command, q, limit, cursor, newest), nil)
		},
	}
	list.Flags().StringVar(&topic, "topic-id", "", "Filter by owning Topic ID")
	get := &cobra.Command{
		Use: "get TASK_ID", Short: "Read a scheduled task", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return operatorRequest(command.Context(), command.OutOrStdout(), endpoint, tokenFile, http.MethodGet, collection+"/"+url.PathEscape(args[0]), nil)
		},
	}
	history := &cobra.Command{
		Use: "trigger-messages TASK_ID", Short: "List Trigger Messages produced by a scheduled task", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			path := collection + "/" + url.PathEscape(args[0]) + "/trigger-messages" + encodeRecordPage(command, url.Values{}, limit, cursor, newest)
			return operatorRequest(command.Context(), command.OutOrStdout(), endpoint, tokenFile, http.MethodGet, path, nil)
		},
	}
	for _, command := range []*cobra.Command{list, history} {
		command.Flags().IntVar(&limit, "limit", 50, "Page size (1–200)")
		command.Flags().StringVar(&cursor, "cursor", "", "Opaque continuation cursor")
		command.Flags().BoolVar(&newest, "newest", false, "List newest records first")
	}
	trigger := &cobra.Command{
		Use: "trigger TASK_ID", Short: "Trigger a scheduled task immediately", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			input := struct {
				OperationID string `json:"operationId"`
			}{OperationID: operationID}
			return operatorRequest(command.Context(), command.OutOrStdout(), endpoint, tokenFile, http.MethodPost, collection+"/"+url.PathEscape(args[0])+"/trigger", input)
		},
	}
	trigger.Flags().StringVar(&operationID, "operation-id", "", "Stable ID for this trigger request")
	_ = trigger.MarkFlagRequired("operation-id")
	cmd.AddCommand(list, get, history, trigger)
	return cmd
}

func encodeRecordPage(command *cobra.Command, q url.Values, limit int, cursor string, newest bool) string {
	if command.Flags().Changed("limit") {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if newest {
		q.Set("order", "newest")
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
