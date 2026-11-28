package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
	"github.com/zoumo/mass/superagent/internal/triggeridentity"
)

// Source identifies a Trigger account.
type Source struct {
	Source    string
	AccountID string
}

type AccessTokenConfig struct {
	Admin    string `mapstructure:"admin"`
	Triggers string `mapstructure:"triggers"`
}

type Config struct {
	Massd            *MassdConfig          `mapstructure:"massd"`
	Sessions         *SessionsConfig       `mapstructure:"sessions"`
	RemoteComputer   *RemoteComputerConfig `mapstructure:"remoteComputer"`
	DataDir          string                `mapstructure:"dataDir"`
	Listen           string                `mapstructure:"listen"`
	ExternalURL      string                `mapstructure:"externalURL"`
	AccessToken      AccessTokenConfig     `mapstructure:"accessToken"`
	adminToken       string
	triggerToken     string
	triggerTokenHash [32]byte
	ReadAllowedHosts []string `mapstructure:"readAllowedHosts"`
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

// LoadComposeConfig reads a Core section from Compose and defaults to embedded MASS.
func LoadComposeConfig(path string, section io.Reader) (Config, error) {
	return loadConfig(path, section)
}

func loadConfig(path string, section io.Reader) (Config, error) {
	var c Config
	composed := section != nil
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetDefault("dataDir", "./superagent-core-data")
	v.SetDefault("listen", "127.0.0.1:24370")
	if !composed {
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
	if err := v.MergeConfigMap(fields); err != nil {
		return c, problem("invalid_argument", "cannot read YAML configuration")
	}
	if composed {
		// Viper omits null and empty mappings from AllSettings. Decode the
		// original mapping so an unknown field cannot hide behind either value.
		c.DataDir = v.GetString("dataDir")
		c.Listen = v.GetString("listen")
		decoder, decodeErr := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			ErrorUnused:      true,
			WeaklyTypedInput: true,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToWeakSliceHookFunc(","),
			),
			Result: &c,
		})
		if decodeErr != nil {
			return c, decodeErr
		}
		err = decoder.Decode(fields)
	} else {
		err = v.UnmarshalExact(&c)
	}
	if err != nil {
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
	if c.Sessions != nil {
		if err := c.Sessions.validate(resolve); err != nil {
			return c, err
		}
		if c.Massd == nil && !composed {
			return c, problem("invalid_argument", "sessions require massd configuration")
		}
	} else if !composed && c.Massd != nil {
		return c, problem("invalid_argument", "massd requires sessions configuration")
	}
	if c.Massd != nil && v.IsSet("massd") {
		var massd MassdConfig
		decodeMassd := func(d *mapstructure.DecoderConfig) {
			strict(d)
			d.DecodeHook = mapstructure.StringToTimeDurationHookFunc()
		}
		if err := v.UnmarshalKey("massd", &massd, decodeMassd); err != nil {
			return c, problem("invalid_argument", "massd configuration contains unknown or invalid fields")
		}
		for _, field := range []string{"sessionIdleTimeout", "writeTimeout"} {
			if value := v.Get("massd.embedded." + field); value != nil {
				if _, ok := value.(string); !ok {
					return c, problem("invalid_argument", "massd.embedded durations must be strings")
				}
			}
		}
		c.Massd = &massd
	}
	if composed {
		if c.Massd != nil && (c.Massd.Endpoint != "" || c.Massd.TokenFile != "") {
			return c, problem("invalid_argument", "Compose uses core.massd.embedded for MASS settings")
		}
		if c.Massd == nil {
			c.Massd = &MassdConfig{}
		}
		if c.Massd.Embedded == nil {
			c.Massd.Embedded = &EmbeddedMassdConfig{}
		}
		if c.Massd.Embedded.DataDir == "" {
			c.Massd.Embedded.DataDir = "./massd-data"
		}
	}
	if c.Massd != nil {
		if err := c.Massd.validate(resolve); err != nil {
			return c, err
		}
	}
	if err := v.UnmarshalKey("accessToken", &c.AccessToken, strict); err != nil {
		return c, problem("invalid_argument", "accessToken must contain admin and triggers file paths")
	}
	if c.DataDir == "" || c.AccessToken.Triggers == "" || c.StorageMaxPages < 0 {
		return c, problem("invalid_argument", "dataDir and accessToken.triggers are required; storageMaxPages must be nonnegative")
	}
	_, portNumber, err := httpaddress.SplitListen(c.Listen)
	if err != nil {
		return c, problem("invalid_argument", "listen "+err.Error())
	}
	if c.ExternalURL != "" || c.Sessions != nil {
		if portNumber == 0 && c.ExternalURL == "" {
			return c, problem("invalid_argument", "sessions require a fixed listen port for role MCP across Core restarts")
		}
		if _, err := c.roleMCPEndpoint(); err != nil {
			return c, err
		}
	}
	if err := loadAccessTokens(&c, resolve); err != nil {
		return c, err
	}
	if _, err := c.massdExposureToken(); err != nil {
		return c, err
	}
	return c, nil
}

var errRemovedPermissionPreset = errors.New("remove permissionPreset")

// takeSessionsConfig decodes sessions before Viper normalizes the rest of the
// configuration. Both entry points use the same strict role field types.
func takeSessionsConfig(fields map[string]any) (*SessionsConfig, error) {
	var sessions *SessionsConfig
	for name, raw := range fields {
		if !strings.EqualFold(name, "sessions") {
			continue
		}
		if sessions != nil {
			return nil, problem("invalid_argument", "configuration contains duplicate sessions fields")
		}
		if _, ok := raw.(map[string]any); !ok {
			return nil, problem("invalid_argument", "sessions must be a mapping")
		}
		sessions = &SessionsConfig{}
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			ErrorUnused: true,
			Result:      sessions,
			RootName:    "sessions",
			DecodeHook: mapstructure.DecodeHookFuncType(func(_, _ reflect.Type, data any) (any, error) {
				mapping, _ := data.(map[string]any)
				for field := range mapping {
					if strings.EqualFold(field, "permissionPreset") {
						return nil, errRemovedPermissionPreset
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

func loadAccessTokens(c *Config, resolve func(string) string) error {
	var err error
	c.AccessToken.Triggers = resolve(c.AccessToken.Triggers)
	c.triggerToken, err = readAccessToken(c.AccessToken.Triggers, "accessToken.triggers")
	if err != nil {
		return err
	}
	c.AccessToken.Admin = resolve(c.AccessToken.Admin)
	if c.AccessToken.Admin != "" {
		c.adminToken, err = ReadOperatorToken(c.AccessToken.Admin)
		if err != nil {
			return err
		}
		if c.adminToken == c.triggerToken {
			return problem("invalid_argument", "accessToken.admin and accessToken.triggers must contain distinct tokens")
		}
	}
	for _, token := range []string{c.triggerToken, c.adminToken} {
		if token != "" && ((c.Massd != nil && token == c.Massd.token) || strings.HasPrefix(token, roleTokenPrefix+"_")) {
			return problem("invalid_argument", "Core access tokens must be distinct from role MCP and MASS credentials")
		}
	}
	c.triggerTokenHash = sha256.Sum256([]byte(c.triggerToken))
	return nil
}

func readAccessToken(path, field string) (string, error) {
	data, err := readPrivate(path)
	if err != nil {
		return "", problem("invalid_argument", field+" must be a readable private regular file")
	}
	token := strings.TrimSpace(string(data))
	if !triggeridentity.ValidToken(token) {
		return "", problem("invalid_argument", field+" requires at least 32 bytes without whitespace or control characters")
	}
	return token, nil
}

// ReadOperatorToken reads the admin token used by Operator CLI commands.
func ReadOperatorToken(path string) (string, error) {
	return readAccessToken(path, "accessToken.admin")
}

// TriggerToken returns the credential for supervised Trigger processes.
func (c Config) TriggerToken() string { return c.triggerToken }

func (c Config) acceptsTriggerToken(token string) bool {
	hash := sha256.Sum256([]byte(token))
	return c.triggerToken != "" && subtle.ConstantTimeCompare(hash[:], c.triggerTokenHash[:]) == 1
}

// CheckUpstreamToken keeps a supervised adapter's native credential separate from Core credentials.
func (c Config) CheckUpstreamToken(token string) error {
	massdToken, err := c.massdExposureToken()
	if err != nil {
		return err
	}
	if token == c.triggerToken || token == c.adminToken || token == massdToken ||
		(c.Massd != nil && token == c.Massd.token) || strings.HasPrefix(token, roleTokenPrefix+"_") {
		return errors.New("native service credentials must be distinct from Core credentials")
	}
	return nil
}
