package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/superagent/internal/triggeridentity"
)

// Source identifies a Trigger account and, for an Outbox consumer, its instance.
type Source struct {
	Source     string `mapstructure:"source"`
	AccountID  string `mapstructure:"accountId"`
	InstanceID string `mapstructure:"instanceId"`
}

type Config struct {
	Massd             *MassdConfig          `mapstructure:"massd"`
	Sessions          *SessionsConfig       `mapstructure:"sessions"`
	RemoteComputer    *RemoteComputerConfig `mapstructure:"remoteComputer"`
	OperatorTokenFile string                `mapstructure:"operatorTokenFile"`
	operatorToken     string
	DataDir           string `mapstructure:"dataDir"`
	Listen            string `mapstructure:"listen"`
	ExternalURL       string `mapstructure:"externalURL"`
	TriggerTokenFile  string `mapstructure:"triggerTokenFile"`
	triggerToken      string
	triggerTokenHash  [32]byte
	OutboxConsumers   []Source `mapstructure:"outboxConsumers"`
	ReadAllowedHosts  []string `mapstructure:"readAllowedHosts"`
	// StorageMaxPages optionally bounds SQLite main database growth (4096-byte pages).
	StorageMaxPages int `mapstructure:"storageMaxPages"`
	// WebOTLPEndpoint is the runtime-only origin of a loopback OTLP receiver.
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
			if !validObserverAuthority(authority) {
				return c, problem("invalid_argument", "readAllowedHosts entries must be DNS names with an optional port")
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
	if c.DataDir == "" || c.TriggerTokenFile == "" || c.StorageMaxPages < 0 {
		return c, problem("invalid_argument", "dataDir and triggerTokenFile are required; storageMaxPages must be nonnegative")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || net.ParseIP(host) == nil || portErr != nil {
		return c, problem("invalid_argument", "listen must use a numeric IP and port")
	}
	if c.ExternalURL != "" || c.Sessions != nil {
		if portNumber == 0 && c.ExternalURL == "" {
			return c, problem("invalid_argument", "sessions require a fixed listen port for role MCP across Core restarts")
		}
		if _, err := c.roleMCPEndpoint(); err != nil {
			return c, err
		}
	}
	if c.operatorToken != "" && !net.ParseIP(host).IsLoopback() {
		return c, problem("invalid_argument", "operator access requires a loopback Core listener")
	}
	c.TriggerTokenFile = resolve(c.TriggerTokenFile)
	tokenBytes, err := readPrivate(c.TriggerTokenFile)
	if err != nil {
		return c, problem("invalid_argument", "triggerTokenFile must be a readable private regular file")
	}
	c.triggerToken = strings.TrimSpace(string(tokenBytes))
	if !triggeridentity.ValidToken(c.triggerToken) {
		return c, problem("invalid_argument", "Trigger token requires at least 32 bytes without whitespace or control characters")
	}
	if c.triggerToken == c.operatorToken || (c.Massd != nil && c.triggerToken == c.Massd.token) ||
		strings.HasPrefix(c.triggerToken, roleTokenPrefix+"_") {
		return c, problem("invalid_argument", "Trigger token must be distinct from Operator, role MCP and MASS credentials")
	}
	c.triggerTokenHash = sha256.Sum256([]byte(c.triggerToken))
	if v.IsSet("outboxConsumers") {
		if err := v.UnmarshalKey("outboxConsumers", &c.OutboxConsumers, strict); err != nil {
			return c, problem("invalid_argument", "outboxConsumers must be a list of source, accountId and instanceId bindings")
		}
	}
	seenConsumers := make(map[[2]string]bool, len(c.OutboxConsumers))
	for _, consumer := range c.OutboxConsumers {
		if !triggeridentity.ValidSource(consumer.Source) ||
			!triggeridentity.ValidAccount(consumer.AccountID) ||
			!triggeridentity.ValidInstance(consumer.InstanceID) {
			return c, problem("invalid_argument", "outboxConsumers requires a valid external source, accountId and instanceId")
		}
		key := [2]string{consumer.Source, consumer.AccountID}
		if seenConsumers[key] {
			return c, problem("invalid_argument", "outboxConsumers allows one instance per source and account")
		}
		seenConsumers[key] = true
	}
	if _, err := c.massdExposureToken(); err != nil {
		return c, err
	}
	return c, nil
}

var dnsLabelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validObserverAuthority(authority string) bool {
	host, port, hasPort := strings.Cut(authority, ":")
	if hasPort {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 || port != strconv.FormatUint(portNumber, 10) {
			return false
		}
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
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
	return fmt.Sprintf("superagent configuration (%d Outbox consumers)", len(c.OutboxConsumers))
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

// TriggerToken returns the shared credential for supervised Trigger processes.
func (c Config) TriggerToken() string { return c.triggerToken }

func (c Config) acceptsTriggerToken(token string) bool {
	hash := sha256.Sum256([]byte(token))
	return c.triggerToken != "" && subtle.ConstantTimeCompare(hash[:], c.triggerTokenHash[:]) == 1
}

// CheckTriggerBinding verifies the identity and any Outbox role of a supervised Trigger.
func (c Config) CheckTriggerBinding(source, account, instance string, requiresOutbox bool) error {
	if !triggeridentity.ValidSource(source) || !triggeridentity.ValidAccount(account) || !triggeridentity.ValidInstance(instance) {
		return errors.New("source, accountId and instanceId must identify an external Trigger")
	}
	if requiresOutbox && c.outboxConsumer(source, account) != instance {
		return errors.New("the reply-enabled Trigger must be the Outbox consumer for its Source and Account")
	}
	return nil
}

// CheckUpstreamToken keeps a supervised adapter's native credential separate from Core credentials.
func (c Config) CheckUpstreamToken(token string) error {
	massdToken, err := c.massdExposureToken()
	if err != nil {
		return err
	}
	if token == c.triggerToken || token == c.operatorToken || token == massdToken ||
		(c.Massd != nil && token == c.Massd.token) || strings.HasPrefix(token, roleTokenPrefix+"_") {
		return errors.New("native service credentials must be distinct from Core credentials")
	}
	return nil
}

func (c Config) outboxConsumer(source, account string) string {
	for _, binding := range c.OutboxConsumers {
		if binding.Source == source && binding.AccountID == account {
			return binding.InstanceID
		}
	}
	return ""
}

func (c Config) hasOutboxConsumer(source, account string) bool {
	return c.outboxConsumer(source, account) != ""
}
