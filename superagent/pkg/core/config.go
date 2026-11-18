package core

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

type Source struct {
	Source     string `mapstructure:"source"`
	AccountID  string `mapstructure:"accountId"`
	InstanceID string `mapstructure:"instanceId"`
	TokenFile  string `mapstructure:"tokenFile"`
	tokenHash  [32]byte
	token      string
}

var instanceIDFormat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Config struct {
	Execution *ExecutionConfig `mapstructure:"execution"`
	DataDir   string           `mapstructure:"dataDir"`
	Listen    string           `mapstructure:"listen"`
	Sources   []Source         `mapstructure:"sources"`
	// StorageMaxPages optionally bounds SQLite main database growth (4096-byte pages).
	StorageMaxPages int `mapstructure:"storageMaxPages"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetDefault("listen", "127.0.0.1:24370")
	if err := v.ReadInConfig(); err != nil {
		return c, problem("invalid_argument", "cannot read YAML configuration")
	}
	if err := v.UnmarshalExact(&c); err != nil {
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
		return p
	}
	c.DataDir = resolve(c.DataDir)
	if c.Execution != nil {
		if err := c.Execution.validate(resolve); err != nil {
			return c, err
		}
	}
	if c.DataDir == "" || len(c.Sources) == 0 || c.StorageMaxPages < 0 {
		return c, problem("invalid_argument", "dataDir and at least one source are required; storageMaxPages must be nonnegative")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil {
		return c, problem("invalid_argument", "listen must use a numeric IP and port")
	}
	if c.Execution != nil && c.Execution.operatorToken != "" && !net.ParseIP(host).IsLoopback() {
		return c, problem("invalid_argument", "operator recovery requires a loopback Core listener")
	}
	seenSource := map[string]string{}
	seenAccount := map[string]string{}
	seenInstance := map[[2]string]bool{}
	seenToken := map[[32]byte]bool{}
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Source == "" || strings.ContainsAny(s.Source, ":/% \t\n") || s.AccountID == "" || s.TokenFile == "" {
			return c, problem("invalid_argument", "source, accountId and tokenFile are required")
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
		if c.Execution != nil && token == c.Execution.operatorToken {
			return c, problem("invalid_argument", "operator token must be distinct from source credentials")
		}
		s.tokenHash = sha256.Sum256([]byte(token))
		if seenToken[s.tokenHash] {
			return c, problem("invalid_argument", "each source credential requires a distinct token")
		}
		seenToken[s.tokenHash] = true
	}
	return c, nil
}

func (c Config) String() string {
	return fmt.Sprintf("superagent configuration (%d sources)", len(c.Sources))
}
