package pipeline

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newExampleCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "example",
		Short: "Output an example pipeline YAML",
		Long:  "Output a starter pipeline YAML to stdout or a file. Edit the generated file to match your workflow.",
		Example: `  # Print example to stdout
  massctl ext pipeline example

  # Write to file
  massctl ext pipeline example -o my-pipeline.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" {
				if err := os.WriteFile(output, examplePipelineYAML, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", output, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote example pipeline to %s\n", output)
				return nil
			}
			_, err := cmd.OutOrStdout().Write(examplePipelineYAML)
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Write to file instead of stdout")
	return cmd
}
