package subcommand

import (
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
	sa "github.com/zoumo/mass/mass-sa"
)

func newOutputCommand() *cobra.Command {
	root := &cobra.Command{Use: "output", Short: "Read durable external outputs and confirm channel outcomes"}
	var configPath, project, consumer, source, thread string
	var after uint64
	var limit int
	read := &cobra.Command{Use: "read", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		query := url.Values{"projectId": {project}, "consumer": {consumer}, "source": {source}, "threadId": {thread}, "limit": {strconv.Itoa(limit)}}
		if cmd.Flags().Changed("after") {
			query.Set("after", strconv.FormatUint(after, 10))
		}
		var result sa.OutputPage
		if err := client.Request(cmd.Context(), "GET", "/outputs?"+query.Encode(), nil, &result); err != nil {
			return err
		}
		printJSON(result)
		return nil
	}}
	var outcome, reference string
	ack := &cobra.Command{Use: "ack <output-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := managementClient(configPath)
		if err != nil {
			return err
		}
		var result sa.OutputConsumer
		if err := client.Request(cmd.Context(), "POST", "/outputs/ack", sa.OutputAckRequest{ProjectID: project, Consumer: consumer, Source: source, ThreadID: thread, OutputID: args[0], Outcome: outcome, Reference: reference}, &result); err != nil {
			return err
		}
		printJSON(result)
		return nil
	}}
	for _, cmd := range []*cobra.Command{read, ack} {
		cmd.Flags().StringVar(&configPath, "config", "", "Configuration path")
		cmd.Flags().StringVar(&project, "project", "mass", "Configured Project ID")
		cmd.Flags().StringVar(&consumer, "consumer", "", "Stable adapter identity")
		cmd.Flags().StringVar(&source, "source", "", "Authorized connector/account identity")
		cmd.Flags().StringVar(&thread, "thread", "", "Authorized source Thread")
		for _, name := range []string{"config", "consumer", "source", "thread"} {
			_ = cmd.MarkFlagRequired(name)
		}
	}
	read.Flags().Uint64Var(&after, "after", 0, "Explicit audit/replay cursor; omit to resume confirmed consumption")
	read.Flags().IntVar(&limit, "limit", 100, "Maximum records (1 through 1000)")
	ack.Flags().StringVar(&outcome, "outcome", "", "delivered, failed, or unknown; all consume without retry")
	ack.Flags().StringVar(&reference, "reference", "", "Channel receipt, error, or reconciliation evidence")
	_ = ack.MarkFlagRequired("outcome")
	_ = ack.MarkFlagRequired("reference")
	root.AddCommand(read, ack)
	return root
}
