package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"
)

func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <pipeline.yaml>",
		Short: "Validate a pipeline YAML file",
		Long: `Validate a pipeline YAML file against the structural schema and semantic rules.

Structural validation (JSON Schema):
  - Required fields, field types, enum values
  - Conditional requirements (e.g., path required for local source)

Semantic validation:
  - Stage name uniqueness and no reserved names
  - All goto targets reference valid stages or __done__/__escalate__
  - All agentRun references exist in agentRuns map
  - All input_from references exist as earlier stages
  - No unreachable stages`,
		Example: `  massctl ext pipeline validate my-pipeline.yaml
  massctl ext pipeline validate ./pipelines/coding-pipeline.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: runValidate,
	}
	return cmd
}

func runValidate(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filePath, err)
	}

	jsonData, err := sigsyaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("parsing YAML: %w", err)
	}

	sch, err := getSchema()
	if err != nil {
		return fmt.Errorf("loading schema: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	if err := sch.Validate(doc); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Schema validation failed:\n%s\n", formatSchemaError(err))
		return fmt.Errorf("schema validation failed")
	}

	var p Pipeline
	if err := sigsyaml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parsing pipeline: %w", err)
	}

	semErrs := ValidateSemantic(&p)
	if len(semErrs) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Semantic validation failed (%d error(s)):\n", len(semErrs))
		for _, e := range semErrs {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", e)
		}
		return fmt.Errorf("semantic validation failed")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Pipeline %q validated successfully.\n", p.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "  Stages: %s\n", formatStageNames(p.Stages))
	return nil
}

func formatSchemaError(err error) string {
	var verr *jsonschema.ValidationError
	if errors.As(err, &verr) {
		basic := verr.BasicOutput()
		var lines []string
		for _, e := range basic.Errors {
			loc := e.InstanceLocation
			if loc == "" {
				loc = "/"
			}
			lines = append(lines, fmt.Sprintf("  - at %s: %s", loc, e.Error))
		}
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("  - %s", err)
}

func formatStageNames(stages []Stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name
	}
	return strings.Join(names, " → ")
}
