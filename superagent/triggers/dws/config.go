package dws

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/superagent/internal/triggeridentity"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

type directConfig struct {
	Enabled bool        `yaml:"enabled"`
	Match   []matchRule `yaml:"match,omitempty"`
}

type selfChatConfig struct {
	Enabled        bool        `yaml:"enabled"`
	ConversationID string      `yaml:"conversationId,omitempty"`
	Match          []matchRule `yaml:"match,omitempty"`
}

type groupConfig struct {
	ConversationID string      `yaml:"conversationId"`
	Match          []matchRule `yaml:"match"`
	Prompt         string      `yaml:"prompt,omitempty"`
	ReplyEnabled   bool        `yaml:"replyEnabled,omitempty"`
}

type matchRule struct {
	Prefix   *string `yaml:"prefix,omitempty"`
	Contains *string `yaml:"contains,omitempty"`
	Regex    *string `yaml:"regex,omitempty"`
	pattern  *regexp.Regexp
}

func (rule *matchRule) UnmarshalYAML(node *yaml.Node) error {
	invalidRule := func() error {
		return fmt.Errorf("line %d: match rule requires exactly one nonempty string matcher", node.Line)
	}
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return invalidRule()
	}
	key, value := node.Content[0], node.Content[1]
	if value.Kind == yaml.AliasNode {
		value = value.Alias
	}
	if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || value.Value == "" {
		return invalidRule()
	}
	switch key.Value {
	case "prefix":
		rule.Prefix = &value.Value
	case "contains":
		rule.Contains = &value.Value
	case "regex":
		rule.Regex = &value.Value
	default:
		return invalidRule()
	}
	return nil
}

func validateRules(scope string, rules []matchRule, required bool) error {
	if required && len(rules) == 0 {
		return fmt.Errorf("%s.match requires at least one rule", scope)
	}
	for i := range rules {
		rule := &rules[i]
		if rule.Prefix == nil && rule.Contains == nil && rule.Regex == nil {
			return fmt.Errorf("%s.match[%d] must configure a matcher", scope, i+1)
		}
		if rule.Regex != nil {
			compiled, err := regexp.Compile(*rule.Regex)
			if err != nil {
				return fmt.Errorf("%s.match[%d] contains an invalid regex", scope, i+1)
			}
			rule.pattern = compiled
		}
	}
	return nil
}

func matchesText(rules []matchRule, text string) bool {
	for _, rule := range rules {
		if rule.Prefix != nil && strings.HasPrefix(text, *rule.Prefix) ||
			rule.Contains != nil && strings.Contains(text, *rule.Contains) ||
			rule.pattern != nil && rule.pattern.MatchString(text) {
			return true
		}
	}
	return false
}

type dwsConfig struct {
	DWS           string          `yaml:"dwsExecutable"`
	Profile       string          `yaml:"profile"`
	Source        string          `yaml:"source"`
	AccountID     string          `yaml:"accountId"`
	SelfID        string          `yaml:"selfOpenDingtalkId"`
	InstanceID    string          `yaml:"instanceId"`
	CoreEndpoint  string          `yaml:"coreEndpoint"`
	TokenEnv      string          `yaml:"coreTokenEnv"`
	Listen        string          `yaml:"statusListen"`
	Direct        directConfig    `yaml:"direct,omitempty"`
	SelfChat      selfChatConfig  `yaml:"selfChat,omitempty"`
	Groups        []groupConfig   `yaml:"groups,omitempty"`
	Feedback      *feedbackConfig `yaml:"feedback,omitempty"`
	DatabasePath  string          `yaml:"databasePath"`
	Capacity      int             `yaml:"bufferCapacity"`
	RetryMillis   int             `yaml:"retryMillis"`
	RequestMillis int             `yaml:"requestTimeoutMillis"`
}

func loadDWSConfig(path string, section io.Reader, coreEndpoint string) (dwsConfig, error) {
	var c dwsConfig
	if err := trigger.DecodeYAML(path, section, &c); err != nil {
		return c, err
	}
	if section != nil {
		c.CoreEndpoint = coreEndpoint
		c.DWS = trigger.ResolveSectionPath(path, c.DWS)
		c.DatabasePath = trigger.ResolveSectionPath(path, c.DatabasePath)
		if c.Feedback != nil {
			c.Feedback.StatePath = trigger.ResolveSectionPath(path, c.Feedback.StatePath)
		}
	}
	c.TokenEnv = trigger.CoreTokenEnv(c.TokenEnv, section != nil)
	if c.Capacity == 0 {
		c.Capacity = 100
	}
	if c.RetryMillis == 0 {
		c.RetryMillis = 1000
	}
	if c.RequestMillis == 0 {
		c.RequestMillis = 5000
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	parts := strings.Split(c.Profile, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] != c.AccountID || strings.Contains(c.Profile, ",") {
		return c, errors.New("profile must be explicit corpId:accountId matching accountId")
	}
	if !triggeridentity.ValidSource(c.Source) || !trigger.ValidEnvVariable(c.TokenEnv) || c.DWS == "" {
		return c, errors.New("valid source, coreTokenEnv and dwsExecutable are required")
	}
	if !triggeridentity.ValidAccount(c.AccountID) || !triggeridentity.ValidInstance(c.InstanceID) {
		return c, errors.New("accountId is required and instanceId must use the Core identifier format")
	}
	if c.SelfID == "" || strings.TrimSpace(c.SelfID) != c.SelfID {
		return c, errors.New("selfOpenDingtalkId is required and must be the profile's exact native openDingTalk ID")
	}
	if !filepath.IsAbs(c.DWS) {
		return c, errors.New("dwsExecutable must be an absolute path")
	}
	if !filepath.IsAbs(c.DatabasePath) {
		return c, errors.New("databasePath must be an absolute path")
	}
	if c.Capacity < 1 || c.Capacity > 10000 || c.RetryMillis < 10 || c.RetryMillis > 60000 || c.RequestMillis < 10 || c.RequestMillis > 60000 {
		return c, errors.New("invalid buffer or timing limits")
	}
	if err := trigger.ValidateCoreEndpoint(c.CoreEndpoint); err != nil {
		return c, err
	}
	if err := trigger.ValidateStatusListen(c.Listen); err != nil {
		return c, err
	}
	if !c.Direct.Enabled && !c.SelfChat.Enabled && len(c.Groups) == 0 {
		return c, errors.New("at least one DWS subscription must be enabled")
	}
	if err := validateRules("direct", c.Direct.Match, c.Direct.Enabled); err != nil {
		return c, err
	}
	if c.SelfChat.Enabled && !validNativeID(c.SelfChat.ConversationID) {
		return c, errors.New("selfChat.conversationId must be the exact native self-chat conversation ID")
	}
	if err := validateRules("selfChat", c.SelfChat.Match, c.SelfChat.Enabled); err != nil {
		return c, err
	}
	seenGroups := map[string]bool{}
	for i, group := range c.Groups {
		scope := fmt.Sprintf("groups[%d]", i+1)
		if !validNativeID(group.ConversationID) || seenGroups[group.ConversationID] ||
			(c.SelfChat.Enabled && group.ConversationID == c.SelfChat.ConversationID) {
			return c, fmt.Errorf("%s.conversationId must be a unique native group conversation ID", scope)
		}
		seenGroups[group.ConversationID] = true
		if err := validateRules(scope, group.Match, true); err != nil {
			return c, err
		}
		if len(group.Prompt) > 4096 {
			return c, fmt.Errorf("%s.prompt exceeds 4096 UTF-8 bytes", scope)
		}
	}
	if c.Feedback != nil {
		if !c.repliesEnabled() {
			return c, errors.New("feedback requires a reply-enabled conversation scope")
		}
		if err := c.Feedback.validate(); err != nil {
			return c, err
		}
		database := filepath.Clean(c.DatabasePath)
		feedbackPath := filepath.Clean(c.Feedback.StatePath)
		evidence := database + ".dws-sends"
		if database == feedbackPath || feedbackPath == evidence || strings.HasPrefix(feedbackPath, evidence+string(filepath.Separator)) {
			return c, errors.New("feedback.statePath must be separate from the queue and send evidence")
		}
	}
	return c, nil
}

func (c dwsConfig) repliesEnabled() bool {
	if c.Direct.Enabled || c.SelfChat.Enabled {
		return true
	}
	for _, group := range c.Groups {
		if group.ReplyEnabled {
			return true
		}
	}
	return false
}

func validNativeID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
