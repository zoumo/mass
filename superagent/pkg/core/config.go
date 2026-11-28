package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/internal/httpaddress"
)

type Source struct {
	Source           string   `mapstructure:"source"`
	AccountID        string   `mapstructure:"accountId"`
	InstanceID       string   `mapstructure:"instanceId"`
	OutboxConsumer   bool     `mapstructure:"outboxConsumer"`
	TokenFile        string   `mapstructure:"tokenFile"`
	ThreadIDs        []string `mapstructure:"threadIds"`
	ThreadIDPrefixes []string `mapstructure:"threadIdPrefixes"`
	tokenHash        [32]byte
	token            string
}

var instanceIDFormat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Config struct {
	Massd             *MassdConfig          `mapstructure:"massd"`
	Sessions          *SessionsConfig       `mapstructure:"sessions"`
	RemoteComputer    *RemoteComputerConfig `mapstructure:"remoteComputer"`
	OperatorTokenFile string                `mapstructure:"operatorTokenFile"`
	operatorToken     string
	DataDir           string   `mapstructure:"dataDir"`
	Listen            string   `mapstructure:"listen"`
	ExternalURL       string   `mapstructure:"externalURL"`
	Sources           []Source `mapstructure:"sources"`
	ReadAllowedHosts  []string `mapstructure:"readAllowedHosts"`
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
	if err := v.MergeConfigMap(fields); err != nil {
		return c, problem("invalid_argument", "cannot read YAML configuration")
	}
	if hasRemovedPermissionPreset(v) {
		return c, problem("invalid_argument", "remove permissionPreset from Superagent session roles and Issue templates; new Sessions use fullAccess")
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
	if c.Sessions != nil {
		var sessions SessionsConfig
		if err := v.UnmarshalKey("sessions", &sessions, strict); err != nil {
			return c, problem("invalid_argument", "sessions configuration contains unknown or invalid fields")
		}
		c.Sessions = &sessions
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
	if c.OperatorTokenFile != "" {
		c.OperatorTokenFile = resolve(c.OperatorTokenFile)
		c.operatorToken, err = ReadOperatorToken(c.OperatorTokenFile)
		if err != nil {
			return c, err
		}
		if c.Massd != nil && c.operatorToken == c.Massd.token {
			return c, problem("invalid_argument", "operator token must be distinct from the massd token")
		}
	}
	if c.DataDir == "" || len(c.Sources) == 0 || c.StorageMaxPages < 0 {
		return c, problem("invalid_argument", "dataDir and at least one source are required; storageMaxPages must be nonnegative")
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
	seenSource := map[string]string{}
	seenAccount := map[string]string{}
	seenInstance := map[[2]string]bool{}
	seenOutboxConsumer := map[string]bool{}
	seenToken := map[[32]byte]bool{}
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Source == "" || strings.ContainsAny(s.Source, ":/% \t\n") || s.AccountID == "" || s.TokenFile == "" {
			return c, problem("invalid_argument", "source, accountId and tokenFile are required")
		}
		for _, id := range append(slices.Clone(s.ThreadIDs), s.ThreadIDPrefixes...) {
			if !validTriggerThreadID(id) {
				return c, problem("invalid_argument", "threadIds and threadIdPrefixes must contain valid IDs or prefixes")
			}
		}
		if account, ok := seenSource[s.Source]; ok && account != s.AccountID {
			return c, problem("invalid_argument", "one source cannot bind multiple accounts")
		}
		if source, ok := seenAccount[s.AccountID]; ok && source != s.Source {
			return c, problem("invalid_argument", "one account cannot bind multiple sources")
		}
		binding := [2]string{s.Source, s.InstanceID}
		if seenInstance[binding] || (s.InstanceID != "" && !instanceIDFormat.MatchString(s.InstanceID)) {
			return c, problem("invalid_argument", "instanceId must be valid and unique within its source")
		}
		if s.OutboxConsumer {
			if s.InstanceID == "" || seenOutboxConsumer[s.AccountID] {
				return c, problem("invalid_argument", "one instance-bound credential per account may set outboxConsumer")
			}
			seenOutboxConsumer[s.AccountID] = true
		}
		seenSource[s.Source] = s.AccountID
		seenAccount[s.AccountID] = s.Source
		seenInstance[binding] = true
		s.TokenFile = resolve(s.TokenFile)
		info, err := os.Lstat(s.TokenFile)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return c, problem("invalid_argument", "source tokenFile must be a private regular file")
		}
		b, err := os.ReadFile(s.TokenFile)
		if err != nil {
			return c, problem("invalid_argument", "cannot read source tokenFile")
		}
		token := strings.TrimSpace(string(b))
		if len(token) < 32 || strings.ContainsAny(token, " \r\n\t") {
			return c, problem("invalid_argument", "source token must have at least 32 non-whitespace bytes")
		}
		s.token = token
		if token == c.operatorToken {
			return c, problem("invalid_argument", "operator token must be distinct from source credentials")
		}
		s.tokenHash = sha256.Sum256([]byte(token))
		if seenToken[s.tokenHash] {
			return c, problem("invalid_argument", "each source credential requires a distinct token")
		}
		seenToken[s.tokenHash] = true
	}
	if _, err := c.massdExposureToken(); err != nil {
		return c, err
	}
	return c, nil
}

func hasRemovedPermissionPreset(v *viper.Viper) bool {
	for _, role := range []string{"router", "topic"} {
		if _, present := v.GetStringMap("sessions.roles." + role)["permissionpreset"]; present {
			return true
		}
	}
	templates, _ := v.Get("sessions.issueTemplates").([]any)
	for _, template := range templates {
		fields, _ := template.(map[string]any)
		if _, present := fields["permissionpreset"]; present {
			return true
		}
	}
	return false
}

func (c Config) String() string {
	return fmt.Sprintf("superagent configuration (%d sources)", len(c.Sources))
}

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

// CheckTriggerBinding verifies the credentials and role a supervised Trigger will use.
func (c Config) CheckTriggerBinding(source, account, instance, token string, requiresOutbox bool) error {
	hash := sha256.Sum256([]byte(token))
	for _, binding := range c.Sources {
		if binding.Source != source || binding.AccountID != account || binding.InstanceID != instance {
			continue
		}
		if subtle.ConstantTimeCompare(hash[:], binding.tokenHash[:]) != 1 {
			return errors.New("configured Core token does not match the source credential")
		}
		if requiresOutbox && c.outboxConsumer(source, account) != instance {
			return errors.New("the reply-enabled Trigger must be the Outbox consumer for its Core source")
		}
		return nil
	}
	return errors.New("source, accountId and instanceId must match a Core source")
}

// outboxConsumer returns the explicitly selected sender, or the sole
// instance-bound credential when no sender has been selected.
func (c Config) outboxConsumer(source, account string) string {
	var instance string
	var candidates int
	for _, s := range c.Sources {
		if s.Source != source || s.AccountID != account || s.InstanceID == "" {
			continue
		}
		if s.OutboxConsumer {
			return s.InstanceID
		}
		instance = s.InstanceID
		candidates++
	}
	if candidates == 1 {
		return instance
	}
	return ""
}

func (c Config) hasOutboxConsumer(source, account string) bool {
	return c.outboxConsumer(source, account) != ""
}
