package cua

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

type fileConfig struct {
	CuaCLI    string           `mapstructure:"cuaCLI"`
	RelayURL  string           `mapstructure:"relayURL"`
	CAFile    string           `mapstructure:"caFile"`
	Computers []computerConfig `mapstructure:"computers"`
}

type computerConfig struct {
	ID        string `mapstructure:"id"`
	TokenFile string `mapstructure:"tokenFile"`
}

type config struct {
	cuaCLI    string
	caFile    string
	computers map[string]*computer
}

type computer struct {
	id    string
	url   string
	token string
}

var machineIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{6,62}[a-z0-9]$`)

func loadConfig(path string) (*config, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("cannot resolve Cua MCP configuration path")
	}
	v := viper.New()
	v.SetConfigFile(absolutePath)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return nil, errors.New("cannot read Cua MCP configuration as YAML")
	}
	var input fileConfig
	if err := v.UnmarshalExact(&input, func(c *mapstructure.DecoderConfig) {
		c.WeaklyTypedInput = false
	}); err != nil {
		return nil, errors.New("configuration must contain known fields with valid types")
	}

	relay, err := parseRelayURL(input.RelayURL)
	if err != nil {
		return nil, err
	}
	if input.CuaCLI == "" || len(input.Computers) == 0 {
		return nil, errors.New("cuaCLI and at least one computer are required")
	}
	baseDir := filepath.Dir(absolutePath)
	cfg := &config{
		cuaCLI:    configPath(baseDir, input.CuaCLI),
		caFile:    configPath(baseDir, input.CAFile),
		computers: make(map[string]*computer, len(input.Computers)),
	}
	for i, item := range input.Computers {
		if !machineIDPattern.MatchString(item.ID) {
			return nil, fmt.Errorf("computers[%d] has an invalid computer id", i)
		}
		if _, exists := cfg.computers[item.ID]; exists {
			return nil, fmt.Errorf("computers[%d] repeats a computer id", i)
		}
		token, err := readToken(configPath(baseDir, item.TokenFile))
		if err != nil {
			return nil, fmt.Errorf("computers[%d] tokenFile: %w", i, err)
		}
		cfg.computers[item.ID] = &computer{id: item.ID, url: relay.JoinPath("m", item.ID).String(), token: token}
	}
	return cfg, nil
}

func configPath(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

func parseRelayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") ||
		u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return nil, errors.New("relayURL must be an origin without credentials, path, query or fragment")
	}
	ip, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || !ip.IsLoopback() && !strings.EqualFold(u.Hostname(), "localhost")) {
		return nil, errors.New("relayURL requires HTTPS, except for HTTP on loopback")
	}
	u.Path = "/"
	return u, nil
}

func readToken(path string) (string, error) {
	// Check the open file so replacing the configured path cannot change what we read.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot open the credential file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("credential file must be a regular file with owner-only permissions")
	}
	const maxTokenBytes = 4096
	raw, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
	if err != nil || len(raw) > maxTokenBytes {
		return "", errors.New("cannot read the credential file")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", errors.New("credential file must contain one non-empty token")
	}
	return token, nil
}

func (cfg *config) computerIDs() []string {
	ids := make([]string, 0, len(cfg.computers))
	for id := range cfg.computers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (cfg *config) resolveComputer(id *string) (*computer, error) {
	if id == nil {
		if len(cfg.computers) == 1 {
			for _, target := range cfg.computers {
				return target, nil
			}
		}
		return nil, errors.New("computer_id is required when multiple computers are configured")
	}
	if target, ok := cfg.computers[*id]; ok {
		return target, nil
	}
	return nil, errors.New("computer_id is not configured")
}
