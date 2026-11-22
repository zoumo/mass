package triggers

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const githubSource = "github"

type githubConfig struct {
	GitHubTokenEnv        string             `yaml:"githubTokenEnv"`
	AccountID             string             `yaml:"accountId"`
	InstanceID            string             `yaml:"instanceId"`
	CoreEndpoint          string             `yaml:"coreEndpoint"`
	AllowInsecureCoreHTTP bool               `yaml:"allowInsecureCoreHTTP,omitempty"`
	TokenEnv              string             `yaml:"coreTokenEnv"`
	Listen                string             `yaml:"statusListen"`
	DatabasePath          string             `yaml:"databasePath"`
	PollSeconds           int                `yaml:"pollIntervalSeconds"`
	RequestMillis         int                `yaml:"requestTimeoutMillis"`
	Repositories          []githubRepository `yaml:"repositories"`
	MatchYAML             yaml.Node          `yaml:"match,omitempty"`
	Match                 githubMatch        `yaml:"-"`
}

type githubRepository struct {
	Repository string `yaml:"repository"`
	Prompt     string `yaml:"prompt,omitempty"`
}

var (
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
	envVariablePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func loadGitHubConfig(path string, section io.Reader, coreEndpoint string) (githubConfig, error) {
	var c githubConfig
	if err := decodeYAML(path, section, &c); err != nil {
		return c, err
	}
	if section != nil {
		c.CoreEndpoint = coreEndpoint
		c.DatabasePath = resolveSectionPath(path, c.DatabasePath)
	}
	match, err := readGitHubMatch(c.MatchYAML)
	if err != nil {
		return c, err
	}
	c.Match = match
	if c.PollSeconds == 0 {
		c.PollSeconds = 60
	}
	if c.RequestMillis == 0 {
		c.RequestMillis = 5000
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	if !filepath.IsAbs(c.DatabasePath) || !instanceIDPattern.MatchString(c.AccountID) || !instanceIDPattern.MatchString(c.InstanceID) || !envVariablePattern.MatchString(c.TokenEnv) || !envVariablePattern.MatchString(c.GitHubTokenEnv) {
		return c, errors.New("GitHub Trigger requires an absolute databasePath, stable accountId and instanceId, coreTokenEnv and githubTokenEnv")
	}
	if c.TokenEnv == c.GitHubTokenEnv {
		return c, errors.New("the Core and GitHub tokens require separate environment variables")
	}
	if c.PollSeconds < 1 || c.PollSeconds > 86400 || c.RequestMillis < 10 || c.RequestMillis > 60000 {
		return c, errors.New("invalid GitHub timing limits")
	}
	if err := validateCoreEndpoint(c.CoreEndpoint, c.AllowInsecureCoreHTTP); err != nil {
		return c, err
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return c, errors.New("statusListen must be numeric loopback")
	}
	if len(c.Repositories) == 0 {
		return c, errors.New("at least one GitHub repository is required")
	}
	seen := make(map[string]bool)
	for i := range c.Repositories {
		repo := &c.Repositories[i]
		repo.Repository = strings.ToLower(repo.Repository)
		if !githubRepositoryPattern.MatchString(repo.Repository) || strings.HasSuffix(repo.Repository, "/.") || strings.HasSuffix(repo.Repository, "/..") || seen[repo.Repository] {
			return c, errors.New("repositories must contain unique GitHub.com owner/repo names")
		}
		if len(repo.Prompt) > 4096 {
			return c, errors.New("repository prompt exceeds 4096 UTF-8 bytes")
		}
		seen[repo.Repository] = true
	}
	return c, nil
}
