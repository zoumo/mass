package runtime

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zoumo/agent-sdk-go/mcp"
)

// ProcessConfig fixes launch inputs without reading provider credentials or data.
//
//nolint:recvcheck // Preserve the public value methods. ResolveEnvironment replaces Env.
type ProcessConfig struct {
	Executable string
	Scenario   string
	Args       []string
	EnvPolicy  string
	Env        map[string]string

	envEntries []mcp.NamedValue
	envFrom    []mcp.ImportSource
}

func ParseProcessConfig(config Config) (ProcessConfig, error) {
	o := ProcessConfig{EnvPolicy: "inherit", Env: map[string]string{}}
	for key, value := range config {
		switch key {
		case "executable", "scenario", "envPolicy":
			s, ok := value.(string)
			if !ok {
				return o, fmt.Errorf("%s must be a string", key)
			}
			switch key {
			case "executable":
				o.Executable = s
			case "scenario":
				o.Scenario = s
			case "envPolicy":
				o.EnvPolicy = s
			}
		case "args":
			switch args := value.(type) {
			case []string:
				o.Args = append([]string{}, args...)
			case []any:
				o.Args = []string{}
				for _, arg := range args {
					s, ok := arg.(string)
					if !ok {
						return o, errors.New("args must contain strings")
					}
					o.Args = append(o.Args, s)
				}
			default:
				return o, errors.New("args must be an array of strings")
			}
		case "env":
			entries, err := decodeProcessEnvList[mcp.NamedValue](key, value)
			if err != nil {
				return o, err
			}
			o.envEntries = entries
		case "envFrom":
			sources, err := decodeProcessEnvList[mcp.ImportSource](key, value)
			if err != nil {
				return o, err
			}
			o.envFrom = sources
		default:
			return o, fmt.Errorf("unknown process config %q", key)
		}
	}
	if _, explicit := config["executable"]; explicit && (!filepath.IsAbs(o.Executable) || strings.ContainsRune(o.Executable, 0)) {
		return o, errors.New("explicit absolute executable required")
	}
	if o.Scenario != "" && (!filepath.IsAbs(o.Scenario) || strings.ContainsRune(o.Scenario, 0)) {
		return o, errors.New("synthetic scenario must be absolute")
	}
	if o.EnvPolicy != "inherit" && o.EnvPolicy != "replace" {
		return o, errors.New("envPolicy must be inherit or replace")
	}
	for _, arg := range o.Args {
		if strings.ContainsRune(arg, 0) {
			return o, errors.New("invalid process argument")
		}
	}
	if err := o.validateEnvironment(); err != nil {
		return o, err
	}
	for _, entry := range o.envEntries {
		if entry.Value != nil {
			o.Env[entry.Name] = *entry.Value
		}
	}
	return o, nil
}

// ResolveExecutable uses the daemon's PATH when no executable was configured.
// It never binds an implicit executable from a relative PATH entry.
func (o ProcessConfig) ResolveExecutable(command string) (string, error) {
	if o.Executable != "" {
		return o.Executable, nil
	}
	path, err := exec.LookPath(command)
	if err != nil || !filepath.IsAbs(path) {
		return "", errors.Join(ErrResourcesReleased, &OperationError{
			Code:    "unavailable",
			Reason:  "agent_executable_unavailable",
			Message: fmt.Sprintf("Agent executable %q was not found on the daemon's absolute PATH entries", command),
		})
	}
	return path, nil
}

// Environment explicitly merges overrides instead of accidentally replacing PATH,
// HOME and other required inherited variables when adding a single setting.
func (o ProcessConfig) Environment(provider string) []string {
	values := map[string]string{}
	if o.EnvPolicy == "inherit" {
		for _, entry := range os.Environ() {
			k, v, _ := strings.Cut(entry, "=")
			// Daemon export configuration can contain Collector credentials.
			// A Runtime can supply its own OpenTelemetry settings explicitly.
			if strings.HasPrefix(k, "OTEL_") {
				continue
			}
			values[k] = v
		}
	}
	maps.Copy(values, o.Env)
	if o.Scenario != "" {
		values[strings.ToUpper(provider)+"_MOCK_SCENARIO"] = o.Scenario
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

// LookupEnv resolves a value from the same environment that Environment
// passes to the provider process. Explicit overrides take precedence.
func (o ProcessConfig) LookupEnv(key string) (string, bool) {
	if value, ok := o.Env[key]; ok {
		return value, true
	}
	if o.EnvPolicy != "inherit" || strings.HasPrefix(key, "OTEL_") {
		return "", false
	}
	return os.LookupEnv(key)
}
