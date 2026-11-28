package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/internal/httpaddress"
)

// Source identifies a Trigger account.
type Source struct {
	Source    string
	AccountID string
}

type Config struct {
	Massd            *MassdConfig          `mapstructure:"massd"`
	Sessions         *SessionsConfig       `mapstructure:"sessions"`
	RemoteComputer   *RemoteComputerConfig `mapstructure:"remoteComputer"`
	DataDir          string                `mapstructure:"dataDir"`
	Listen           string                `mapstructure:"listen"`
	ExternalURL      string                `mapstructure:"externalURL"`
	ReadAllowedHosts []string              `mapstructure:"readAllowedHosts"`
	// StorageMaxPages optionally bounds SQLite main database growth (4096-byte pages).
	StorageMaxPages int `mapstructure:"storageMaxPages"`
	// WebOTLPEndpoint is the runtime-only base URL of the Web OTLP receiver.
	WebOTLPEndpoint string             `mapstructure:"-"`
	OpenObserve     *OpenObserveConfig `mapstructure:"openobserve"`
	// ComposeTriggerStatus reads Trigger observations from the local supervisor.
	ComposeTriggerStatus func(context.Context) TriggerStatuses           `mapstructure:"-"`
	ComposeWatchdog      func(context.Context) (WatchdogSnapshot, error) `mapstructure:"-"`
}

func LoadConfig(path string) (Config, error) {
	return loadConfig(path, nil)
}

// LoadComposeConfig reads a Core section from Compose.
func LoadComposeConfig(path string, section io.Reader) (Config, error) {
	return loadConfig(path, section)
}

func loadConfig(path string, section io.Reader) (Config, error) {
	var c Config
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetDefault("dataDir", "./superagent-core-data")
	v.SetDefault("listen", "127.0.0.1:24370")
	if section == nil {
		file, err := os.Open(path)
		if err != nil {
			return c, problem("invalid_argument", "cannot read YAML configuration")
		}
		defer file.Close()
		section = file
	}
	var fields map[string]any
	if err := yaml.NewDecoder(section).Decode(&fields); err != nil {
		return c, problem("invalid_argument", "cannot read YAML configuration")
	}
	// Viper normalizes keys in place. Validate this section against the original
	// YAML fields so a case-insensitive duplicate cannot hide an invalid value.
	remoteComputer, err := decodeRemoteComputer(fields)
	if err != nil {
		return c, err
	}
	sessions, err := takeSessionsConfig(fields)
	if err != nil {
		return c, err
	}
	massd, err := takeMassdConfig(fields)
	if err != nil {
		return c, err
	}
	if err := v.MergeConfigMap(fields); err != nil {
		return c, problem("invalid_argument", "cannot read YAML configuration")
	}
	// Decode the original mapping so unknown empty and null fields are rejected.
	c.DataDir = v.GetString("dataDir")
	c.Listen = v.GetString("listen")
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		ErrorUnused:      true,
		WeaklyTypedInput: true,
		DecodeHook:       mapstructure.StringToWeakSliceHookFunc(","),
		Result:           &c,
	})
	if err != nil {
		return c, err
	}
	if err := decoder.Decode(fields); err != nil {
		return c, problem("invalid_argument", "configuration contains unknown or invalid fields")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	resolve := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			return filepath.Join(base, p)
		}
		if p != "" {
			return filepath.Clean(p)
		}
		return p
	}
	c.DataDir = resolve(c.DataDir)
	c.RemoteComputer = remoteComputer
	if c.RemoteComputer != nil {
		if err := c.RemoteComputer.resolvePaths(resolve); err != nil {
			return c, err
		}
	}
	if c.OpenObserve != nil {
		c.OpenObserve.CredentialFile = resolve(c.OpenObserve.CredentialFile)
	}
	strict := func(d *mapstructure.DecoderConfig) {
		d.ErrorUnused = true
		d.WeaklyTypedInput = false
		d.DecodeHook = nil
	}
	if v.IsSet("readAllowedHosts") {
		if err := v.UnmarshalKey("readAllowedHosts", &c.ReadAllowedHosts, strict); err != nil {
			return c, problem("invalid_argument", "readAllowedHosts must be a list of strings")
		}
		for _, authority := range c.ReadAllowedHosts {
			if !httpaddress.ValidAuthority(authority) {
				return c, problem("invalid_argument", "readAllowedHosts entries must be DNS names or IP addresses with an optional port")
			}
		}
	}
	c.Sessions = sessions
	if c.Sessions.Router.Workspace == "" {
		c.Sessions.Router.Workspace = filepath.Join(c.DataDir, routerWorkspaceDir)
	} else {
		c.Sessions.Router.Workspace = resolve(c.Sessions.Router.Workspace)
	}
	if err := c.Sessions.validate(); err != nil {
		return c, err
	}
	c.Massd = massd
	if c.Massd != nil {
		if err := c.Massd.validateEndpoint(); err != nil {
			return c, err
		}
		c.Massd.TokenFile = resolve(c.Massd.TokenFile)
	}
	if c.DataDir == "" || c.StorageMaxPages < 0 {
		return c, problem("invalid_argument", "dataDir is required and storageMaxPages must be nonnegative")
	}
	_, portNumber, err := httpaddress.SplitListen(c.Listen)
	if err != nil {
		return c, problem("invalid_argument", "listen "+err.Error())
	}
	if portNumber == 0 && c.ExternalURL == "" {
		return c, problem("invalid_argument", "sessions require a fixed listen port for role MCP across Core restarts")
	}
	if _, err := c.roleMCPEndpoint(); err != nil {
		return c, err
	}
	return c, nil
}

var errRemovedPermissionPreset = errors.New("remove permissionPreset")

const routerWorkspaceDir = "router-workspace"

// takeSessionsConfig decodes sessions before Viper normalizes the rest of the
// configuration. Both entry points use the same strict role field types.
func takeSessionsConfig(fields map[string]any) (*SessionsConfig, error) {
	sessions := &SessionsConfig{
		Router: RouterSessionConfig{ExecutionProfile: ExecutionProfile{RuntimeRef: "claude"}},
		Topic:  ExecutionProfile{RuntimeRef: "codex"},
	}
	var found bool
	for name, raw := range fields {
		if !strings.EqualFold(name, "sessions") {
			continue
		}
		if found {
			return nil, problem("invalid_argument", "configuration contains duplicate sessions fields")
		}
		found = true
		if _, ok := raw.(map[string]any); !ok {
			return nil, problem("invalid_argument", "sessions must be a mapping")
		}
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			ErrorUnused: true,
			Result:      sessions,
			RootName:    "sessions",
			DecodeHook: mapstructure.DecodeHookFuncType(func(_, _ reflect.Type, data any) (any, error) {
				mapping, _ := data.(map[string]any)
				for field, value := range mapping {
					if strings.EqualFold(field, "permissionPreset") {
						return nil, errRemovedPermissionPreset
					}
					if value == nil {
						return nil, errors.New("session fields must not be null")
					}
				}
				return data, nil
			}),
		})
		if err != nil {
			return nil, err
		}
		if err := decoder.Decode(raw); err != nil {
			if field, ok := errors.AsType[*mapstructure.DecodeError](err); ok {
				if errors.Is(field, errRemovedPermissionPreset) {
					return nil, problem("invalid_argument", "remove permissionPreset from "+field.Name()+". New Sessions use fullAccess")
				}
				return nil, problem("invalid_argument", field.Name()+" contains an unknown field or invalid value")
			}
			return nil, problem("invalid_argument", "sessions contains unknown or invalid fields")
		}
		delete(fields, name)
	}
	if sessions.Issue.Templates == nil {
		sessions.Issue.Templates = []IssueTemplate{{Name: "default", ExecutionProfile: ExecutionProfile{RuntimeRef: "codex"}}}
	}
	return sessions, nil
}

func (c Config) String() string { return "superagent configuration" }

const mcpCredentialsDir = "mcp-files"

func (c Config) prepareMCPFiles() error {
	if c.Sessions == nil {
		return nil
	}
	dir := filepath.Join(c.DataDir, mcpCredentialsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return problem("unavailable", "cannot prepare MCP credentials directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return problem("failed_precondition", "MCP credentials directory must be private")
	}
	return nil
}
