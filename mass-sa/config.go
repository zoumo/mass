package sa

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

const (
	// RouterNamespace is the MASS namespace that holds project routers.
	RouterNamespace = "sa-system"

	maxConfigBytes             = 1 << 20
	defaultInboxMergeWindowMS  = 1000
	defaultInboxMaxBatchSize   = 8
	defaultInboxMaxPromptBytes = 96 << 10
	defaultManagementListen    = "127.0.0.1:8787"
)

var (
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	envNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config is the validated service configuration.
type Config struct {
	StateFile  string           `yaml:"stateFile"`
	MASS       MASSConfig       `yaml:"mass"`
	Multica    MulticaConfig    `yaml:"multica"`
	Management ManagementConfig `yaml:"management"`
	Projects   []ProjectConfig  `yaml:"projects"`
}

// MASSConfig identifies the massd endpoint and router namespace.
type MASSConfig struct {
	Endpoint  string `yaml:"endpoint"`
	TokenEnv  string `yaml:"tokenEnv,omitempty"`
	TokenFile string `yaml:"tokenFile,omitempty"`
	Namespace string `yaml:"namespace"`
}

// MulticaConfig contains settings shared by all projects.
type MulticaConfig struct {
	Executable string `yaml:"executable"`
}

// ManagementConfig controls the authenticated management HTTP surface. The
// internal tool endpoint shares the same loopback listener under /v1/tools.
type ManagementConfig struct {
	Listen    string `yaml:"listen,omitempty"`
	TokenEnv  string `yaml:"tokenEnv,omitempty"`
	TokenFile string `yaml:"tokenFile,omitempty"`
}

// ProjectConfig defines one long-lived SuperAgent project.
type ProjectConfig struct {
	ID         string         `yaml:"id"`
	Name       string         `yaml:"name"`
	CWD        string         `yaml:"cwd"`
	Memory     string         `yaml:"memory"`
	RuntimeRef string         `yaml:"runtimeRef"`
	Router     RouterConfig   `yaml:"router"`
	Worker     RoleConfig     `yaml:"worker"`
	Inbox      InboxConfig    `yaml:"inbox"`
	Multica    ProjectMultica `yaml:"multica"`
}

// RouterConfig controls the project router Session.
type RouterConfig struct {
	Name   string `yaml:"name,omitempty"`
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
}

// RoleConfig is the literal model and effort requested for a Session role.
type RoleConfig struct {
	Model       string   `yaml:"model,omitempty"`
	Effort      string   `yaml:"effort,omitempty"`
	RuntimeRefs []string `yaml:"runtimeRefs,omitempty"`
}

// InboxConfig controls durable event batching for one project.
type InboxConfig struct {
	MergeWindowMS  int `yaml:"mergeWindowMs"`
	MaxBatchSize   int `yaml:"maxBatchSize"`
	MaxPromptBytes int `yaml:"maxPromptBytes"`
}

// ProjectMultica identifies the project's Multica target.
type ProjectMultica struct {
	WorkspaceID string `yaml:"workspaceId"`
	ProjectID   string `yaml:"projectId"`
}

// LoadConfig reads, defaults, normalizes, and validates a YAML configuration.
func LoadConfig(path string) (Config, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration path is invalid")
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration file must be a regular file no larger than 1 MiB")
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration file is unavailable")
	}

	if err := validateYAMLDocument(content); err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration YAML is invalid: "+err.Error())
	}
	reader := viper.New()
	reader.SetConfigType("yaml")
	if err := reader.ReadConfig(bytes.NewReader(content)); err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration YAML is invalid: "+err.Error())
	}
	var cfg Config
	if err := reader.UnmarshalExact(&cfg, func(decoder *mapstructure.DecoderConfig) {
		decoder.TagName = "yaml"
		decoder.WeaklyTypedInput = false
		decoder.DecodeHook = nil
	}); err != nil {
		return Config{}, operationError("invalid_argument", "invalid_config", "configuration YAML is invalid: "+err.Error())
	}

	cfg.applyDefaults()
	if err := cfg.normalize(filepath.Dir(absolutePath)); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateYAMLDocument(content []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return err
	}
	if len(root.Content) == 0 {
		return errors.New("configuration must contain one YAML document")
	}
	if err := validateYAMLNode(&root); err != nil {
		return err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple YAML documents are not supported")
		}
		return err
	}
	return nil
}

func validateYAMLNode(node *yaml.Node) error {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLNode(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := map[string]struct{}{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate key %q", key.Value)
			}
			seen[key.Value] = struct{}{}
			if err := validateYAMLNode(node.Content[index+1]); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		if node.Alias != nil {
			return validateYAMLNode(node.Alias)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Management.Listen == "" {
		c.Management.Listen = defaultManagementListen
	}
	for i := range c.Projects {
		project := &c.Projects[i]
		if project.Name == "" {
			project.Name = project.ID
		}
		if project.Router.Effort == "" {
			project.Router.Effort = "low"
		}
		if project.Worker.Effort == "" {
			project.Worker.Effort = "high"
		}
		if project.Inbox.MergeWindowMS == 0 {
			project.Inbox.MergeWindowMS = defaultInboxMergeWindowMS
		}
		if project.Inbox.MaxBatchSize == 0 {
			project.Inbox.MaxBatchSize = defaultInboxMaxBatchSize
		}
		if project.Inbox.MaxPromptBytes == 0 {
			project.Inbox.MaxPromptBytes = defaultInboxMaxPromptBytes
		}
	}
}

func (c *Config) normalize(baseDir string) error {
	endpoint, err := normalizeMASSEndpoint(c.MASS.Endpoint)
	if err != nil {
		return err
	}
	c.MASS.Endpoint = endpoint
	if c.StateFile == "" {
		return invalidConfig("stateFile is required")
	}
	c.StateFile = resolveConfigPath(baseDir, c.StateFile)
	if c.MASS.TokenFile != "" {
		c.MASS.TokenFile = resolveConfigPath(baseDir, c.MASS.TokenFile)
	}
	if c.Management.TokenFile != "" {
		c.Management.TokenFile = resolveConfigPath(baseDir, c.Management.TokenFile)
	}
	return nil
}

func resolveConfigPath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func normalizeMASSEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return "", invalidConfig("mass.endpoint must be a ws:// or wss:// endpoint")
	}
	switch parsed.Scheme {
	case "ws", "wss":
	default:
		return "", invalidConfig("mass.endpoint must be a ws:// or wss:// endpoint")
	}
	switch strings.TrimSuffix(parsed.Path, "/") {
	case "":
		parsed.Path = "/rpc"
	case "/rpc":
		parsed.Path = "/rpc"
	default:
		return "", invalidConfig("mass.endpoint must use the /rpc path")
	}
	return parsed.String(), nil
}

// Validate checks a normalized configuration without reading credentials.
func (c Config) Validate() error {
	return c.validate()
}

func (c Config) validate() error {
	if c.StateFile == "" || !filepath.IsAbs(c.StateFile) {
		return invalidConfig("stateFile must resolve to an absolute path")
	}
	if _, err := normalizeMASSEndpoint(c.MASS.Endpoint); err != nil {
		return err
	}
	if c.MASS.Namespace != RouterNamespace {
		return invalidConfig("mass.namespace must be " + RouterNamespace)
	}
	if c.MASS.TokenEnv != "" && c.MASS.TokenFile != "" {
		return invalidConfig("mass.tokenEnv and mass.tokenFile are mutually exclusive")
	}
	if c.MASS.TokenEnv != "" && !envNamePattern.MatchString(c.MASS.TokenEnv) {
		return invalidConfig("mass.tokenEnv is not a valid environment variable name")
	}
	if c.MASS.TokenFile != "" && !filepath.IsAbs(c.MASS.TokenFile) {
		return invalidConfig("mass.tokenFile must resolve to an absolute path")
	}
	if err := validateLoopbackListen(c.Management.Listen); err != nil {
		return err
	}
	if c.Management.TokenEnv != "" && c.Management.TokenFile != "" {
		return invalidConfig("management.tokenEnv and management.tokenFile are mutually exclusive")
	}
	if c.Management.TokenEnv != "" && !envNamePattern.MatchString(c.Management.TokenEnv) {
		return invalidConfig("management.tokenEnv is not a valid environment variable name")
	}
	if c.Management.TokenFile != "" && !filepath.IsAbs(c.Management.TokenFile) {
		return invalidConfig("management.tokenFile must resolve to an absolute path")
	}
	if err := validateText("multica.executable", c.Multica.Executable, true); err != nil {
		return err
	}
	if len(c.Projects) == 0 {
		return invalidConfig("at least one project is required")
	}

	projectIDs := make(map[string]struct{}, len(c.Projects))
	routerNames := make(map[string]struct{}, len(c.Projects))
	for i, project := range c.Projects {
		prefix := fmt.Sprintf("projects[%d]", i)
		if !validDNSLabel(project.ID) {
			return invalidConfig(prefix + ".id must be a lowercase DNS label")
		}
		if _, exists := projectIDs[project.ID]; exists {
			return invalidConfig("project ids must be unique")
		}
		projectIDs[project.ID] = struct{}{}
		if err := validateText(prefix+".name", project.Name, true); err != nil {
			return err
		}
		if !filepath.IsAbs(project.CWD) {
			return invalidConfig(prefix + ".cwd must be absolute")
		}
		if err := validateText(prefix+".memory", project.Memory, true); err != nil {
			return err
		}
		if strings.TrimSpace(project.Memory) == "" {
			return invalidConfig(prefix + ".memory must not be blank")
		}
		if err := validateText(prefix+".runtimeRef", project.RuntimeRef, true); err != nil {
			return err
		}
		routerName := RouterName(project)
		if !validDNSSubdomain(routerName) || uuidPattern.MatchString(routerName) {
			return invalidConfig(prefix + ".router.name must be a non-UUID DNS subdomain")
		}
		if _, exists := routerNames[routerName]; exists {
			return invalidConfig("router names must be unique within " + RouterNamespace)
		}
		routerNames[routerName] = struct{}{}
		if err := validateText(prefix+".router.name", project.Router.Name, false); err != nil {
			return err
		}
		if err := validateRole(prefix+".router", RoleConfig{Model: project.Router.Model, Effort: project.Router.Effort}); err != nil {
			return err
		}
		if err := validateRole(prefix+".worker", project.Worker); err != nil {
			return err
		}
		for _, runtimeRef := range project.Worker.RuntimeRefs {
			if err := validateText(prefix+".worker.runtimeRefs", runtimeRef, true); err != nil {
				return err
			}
			if strings.TrimSpace(runtimeRef) == "" {
				return invalidConfig(prefix + ".worker.runtimeRefs must not contain blank names")
			}
		}
		if project.Inbox.MergeWindowMS <= 0 {
			return invalidConfig(prefix + ".inbox.mergeWindowMs must be positive")
		}
		if project.Inbox.MaxBatchSize <= 0 {
			return invalidConfig(prefix + ".inbox.maxBatchSize must be positive")
		}
		if project.Inbox.MaxPromptBytes <= 0 {
			return invalidConfig(prefix + ".inbox.maxPromptBytes must be positive")
		}
		if err := validateText(prefix+".multica.workspaceId", project.Multica.WorkspaceID, true); err != nil {
			return err
		}
		if err := validateText(prefix+".multica.projectId", project.Multica.ProjectID, true); err != nil {
			return err
		}
	}
	return nil
}

func validateLoopbackListen(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return invalidConfig("management.listen must be a loopback host:port")
	}
	if portValue, err := net.LookupPort("tcp", port); err != nil || portValue < 0 || portValue > 65535 {
		return invalidConfig("management.listen port is invalid")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return invalidConfig("management.listen must bind a loopback address")
	}
	return nil
}

// ManagementEndpoint returns the loopback management API base URL. A named TCP
// service port in management.listen is resolved to its numeric port so the
// endpoint is always a URL url.Parse accepts.
func (c Config) ManagementEndpoint() string {
	return "http://" + resolvedManagementAddress(c.Management.Listen) + "/v1/management"
}

// resolvedManagementAddress replaces a named TCP service port with its numeric
// port, leaving numeric ports and unresolved service names unchanged.
func resolvedManagementAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if portValue, err := net.LookupPort("tcp", port); err == nil && portValue > 0 && portValue <= 65535 {
		port = strconv.Itoa(portValue)
	}
	return net.JoinHostPort(host, port)
}

// ResolveManagementToken reads the configured management bearer token. Token
// material is never returned in errors or written to state.
func (c Config) ResolveManagementToken() (string, error) {
	switch {
	case c.Management.TokenEnv != "" && c.Management.TokenFile != "":
		return "", invalidConfig("management.tokenEnv and management.tokenFile are mutually exclusive")
	case c.Management.TokenEnv != "":
		token, exists := os.LookupEnv(c.Management.TokenEnv)
		if !exists || strings.TrimSpace(token) == "" {
			return "", operationError("failed_precondition", "management_token_missing", "management token environment variable is not set")
		}
		return strings.TrimSpace(token), nil
	case c.Management.TokenFile != "":
		info, err := os.Stat(c.Management.TokenFile)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", operationError("failed_precondition", "management_token_missing", "management token file must be a regular 0600 file")
		}
		content, err := os.ReadFile(c.Management.TokenFile)
		if err != nil {
			return "", operationError("failed_precondition", "management_token_missing", "management token file is unavailable")
		}
		token := strings.TrimSpace(string(content))
		if token == "" {
			return "", operationError("failed_precondition", "management_token_missing", "management token file is empty")
		}
		return token, nil
	default:
		return "", operationError("failed_precondition", "management_token_missing", "management.tokenEnv or management.tokenFile is required")
	}
}

func validateRole(name string, role RoleConfig) error {
	if err := validateText(name+".model", role.Model, false); err != nil {
		return err
	}
	if err := validateText(name+".effort", role.Effort, true); err != nil {
		return err
	}
	if role.Model != "" && strings.TrimSpace(role.Model) == "" {
		return invalidConfig(name + ".model must not be blank")
	}
	if strings.TrimSpace(role.Effort) == "" {
		return invalidConfig(name + ".effort must not be blank")
	}
	return nil
}

func validateText(name, value string, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return invalidConfig(name + " must be valid UTF-8 without NUL")
	}
	if required && value == "" {
		return invalidConfig(name + " is required")
	}
	return nil
}

func invalidConfig(message string) error {
	return operationError("invalid_argument", "invalid_config", message)
}

func validDNSLabel(value string) bool {
	return len(value) <= 63 && dnsLabelPattern.MatchString(value)
}

func validDNSSubdomain(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
