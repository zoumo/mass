package runtime

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/zoumo/agent-sdk-go/agent"
)

// InitialSessionConfig is the durable, literal request for the main Session.
// It is not an observation of the current native configuration.
type InitialSessionConfig struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// SDKConfig maps directly to the SDK. Empty fields mean omission, never reset.
func (c *InitialSessionConfig) SDKConfig() agent.SessionConfig {
	if c == nil {
		return agent.SessionConfig{}
	}
	return agent.SessionConfig{Model: c.Model, Effort: c.Effort}
}

func (c *InitialSessionConfig) Validate() error {
	if err := c.SDKConfig().Validate(); err != nil {
		return InvalidArgument("invalid initial session config: " + err.Error())
	}
	return nil
}

// UnmarshalJSON rejects values encoding/json would silently replace or coerce.
func (c *InitialSessionConfig) UnmarshalJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return InvalidArgument("invalid config encoding")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return InvalidArgument("config must be an object")
	}
	for key, value := range fields {
		if key != "model" && key != "effort" {
			return InvalidArgument("unknown config field")
		}
		if len(value) == 0 || value[0] != '"' || !validConfigEscapes(value) {
			return InvalidArgument("config value must be an encoded string")
		}
	}
	type plain InitialSessionConfig
	var result plain
	if err := json.Unmarshal(raw, &result); err != nil {
		return InvalidArgument("invalid config")
	}
	next := InitialSessionConfig(result)
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

// JSON permits escaped surrogate pairs, but isolated surrogates are not Unicode.
func validConfigEscapes(raw []byte) bool {
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if v >= 0xDC00 && v <= 0xDFFF {
			return false
		}
		if v < 0xD800 || v > 0xDBFF {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}
