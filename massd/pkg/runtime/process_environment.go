package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-go/mcp"

	"github.com/zoumo/mass/massd/pkg/events"
)

func decodeProcessEnvList[T any](field string, value any) ([]T, error) {
	entries, ok := value.([]any)
	if !ok || entries == nil {
		return nil, fmt.Errorf("%s must be an array of objects", field)
	}
	if err := events.CheckDynamicJSON(entries); err != nil {
		return nil, fmt.Errorf("invalid %s entries", field)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("invalid %s entries", field)
	}
	var decoded []T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("invalid %s entries", field)
	}
	return decoded, nil
}

// The SDK exposes file value resolution through its stdio environment model.
func (o ProcessConfig) environmentSource() []mcp.Server {
	return []mcp.Server{{
		Name: "runtime", Transport: mcp.TransportStdio, Command: "agent",
		Env: o.envEntries, EnvFrom: o.envFrom,
	}}
}

func (o ProcessConfig) validateEnvironment() error {
	if err := mcp.ValidateServers(o.environmentSource()); err != nil {
		return errors.New(processEnvironmentMessage(err))
	}
	for _, entry := range o.envEntries {
		if entry.ValueFrom != nil && !filepath.IsAbs(entry.ValueFrom.FileKeyRef.Path) {
			return errors.New("env fileKeyRef path must be absolute")
		}
	}
	for _, source := range o.envFrom {
		if !filepath.IsAbs(source.FileRef.Path) {
			return errors.New("envFrom fileRef path must be absolute")
		}
	}
	return nil
}

// ResolveEnvironment reads file references for a new Driver. The Runtime
// resource configuration retains its references.
func (o *ProcessConfig) ResolveEnvironment() error {
	if len(o.envEntries) == 0 && len(o.envFrom) == 0 {
		return nil
	}
	resolved, err := mcp.ResolveServers(o.environmentSource(), nil)
	if err != nil {
		return errors.Join(ErrResourcesReleased, &OperationError{
			Code:    "invalid_argument",
			Reason:  "runtime_environment_invalid",
			Message: processEnvironmentMessage(err),
		})
	}
	values, err := mcp.LiteralValues(resolved[0].Env)
	if err != nil {
		return errors.Join(ErrResourcesReleased, InvalidArgument("runtime environment did not resolve to literal values"))
	}
	o.Env = values
	return nil
}

func processEnvironmentMessage(err error) string {
	var validation *mcp.ValidationError
	if errors.As(err, &validation) {
		return "Runtime " + strings.TrimPrefix(validation.Message, "MCP server 0 field ")
	}
	return "Runtime environment is invalid"
}
