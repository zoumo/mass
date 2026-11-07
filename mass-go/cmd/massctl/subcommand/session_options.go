package subcommand

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type sessionOptionsInput struct {
	features, mcpServers string
}

func (c *sessionOptionsInput) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.features, "features-json", "", "Session features JSON object or null")
	cmd.Flags().StringVar(&c.mcpServers, "mcp-servers-json", "", "Session MCP servers JSON array or null")
}

func (c *sessionOptionsInput) provided(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("features-json") || cmd.Flags().Changed("mcp-servers-json")
}

func (c *sessionOptionsInput) read(cmd *cobra.Command) (runtime.SessionDefaults, error) {
	var defaults runtime.SessionDefaults
	if cmd.Flags().Changed("features-json") {
		value, err := parseCLIJSON[runtime.SessionFeatures](c.features)
		if err != nil {
			return defaults, invalid("Invalid session features JSON")
		}
		defaults.Features = value
	}
	if cmd.Flags().Changed("mcp-servers-json") {
		value, err := parseCLIJSON[[]runtime.MCPServer](c.mcpServers)
		if err != nil {
			return defaults, invalid("Invalid session MCP servers JSON")
		}
		defaults.MCPServers = value
		if value != nil {
			if err := runtime.ValidateMCPServers(*value); err != nil {
				return defaults, invalid("Invalid session MCP servers JSON")
			}
		}
	}
	return defaults, nil
}

func parseCLIJSON[T any](raw string) (*T, error) {
	if !utf8.ValidString(raw) || transport.ValidateJSON([]byte(raw)) != nil {
		return nil, invalid("Invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var value *T
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalid("Expected one JSON value")
	}
	return value, nil
}
