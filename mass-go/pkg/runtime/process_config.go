package runtime

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProcessConfig fixes launch inputs without reading provider credentials or data.
type ProcessConfig struct {
	Executable string
	Scenario   string
	Args       []string
	EnvPolicy  string
	Env        map[string]string
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
			switch env := value.(type) {
			case map[string]string:
				maps.Copy(o.Env, env)
			case map[string]any:
				for k, v := range env {
					s, ok := v.(string)
					if !ok {
						return o, errors.New("env values must be strings")
					}
					o.Env[k] = s
				}
			default:
				return o, errors.New("env must be a string map")
			}
		default:
			return o, fmt.Errorf("unknown process config %q", key)
		}
	}
	if !filepath.IsAbs(o.Executable) || strings.ContainsRune(o.Executable, 0) {
		return o, errors.New("explicit absolute executable required")
	}
	if o.Scenario != "" && (!filepath.IsAbs(o.Scenario) || strings.ContainsRune(o.Scenario, 0)) {
		return o, errors.New("synthetic scenario must be absolute")
	}
	if o.EnvPolicy != "inherit" && o.EnvPolicy != "replace" {
		return o, errors.New("envPolicy must be inherit or replace")
	}
	for key, value := range o.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return o, errors.New("invalid environment entry")
		}
	}
	for _, arg := range o.Args {
		if strings.ContainsRune(arg, 0) {
			return o, errors.New("invalid process argument")
		}
	}
	return o, nil
}

// Environment explicitly merges overrides instead of accidentally replacing PATH,
// HOME and other required inherited variables when adding a single setting.
func (o ProcessConfig) Environment(provider string) []string {
	values := map[string]string{}
	if o.EnvPolicy == "inherit" {
		for _, entry := range os.Environ() {
			k, v, _ := strings.Cut(entry, "=")
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
