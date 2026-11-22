package core

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/massd"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type MassdConfig struct {
	Embedded  *EmbeddedMassdConfig `mapstructure:"embedded"`
	Endpoint  string               `mapstructure:"endpoint"`
	TokenFile string               `mapstructure:"tokenFile"`
	token     string
}

type EmbeddedMassdConfig struct {
	DataDir                         string           `mapstructure:"dataDir"`
	MetaStore                       metastore.Config `mapstructure:"metaStore"`
	SessionIdleTimeout              *time.Duration   `mapstructure:"sessionIdleTimeout"`
	WriteTimeout                    *time.Duration   `mapstructure:"writeTimeout"`
	PrototypeAutoApprovePermissions bool             `mapstructure:"prototypeAutoApprovePermissions"`
	Expose                          *MassdExposure   `mapstructure:"expose"`
}

type MassdExposure struct {
	TokenFile      string   `mapstructure:"tokenFile"`
	AllowedOrigins []string `mapstructure:"allowedOrigins"`
}

func (c *MassdConfig) validate(resolve func(string) string, composed bool) error {
	if c.Embedded != nil {
		if composed || c.Endpoint != "" || c.TokenFile != "" {
			return problem("invalid_argument", "massd.embedded cannot be combined with compose, massd.endpoint or massd.tokenFile")
		}
		c.Embedded.DataDir = resolve(c.Embedded.DataDir)
		if c.Embedded.Expose != nil {
			c.Embedded.Expose.TokenFile = resolve(c.Embedded.Expose.TokenFile)
		}
		return c.Embedded.validate()
	}
	if !composed {
		if _, err := client.WebSocketEndpoint(c.Endpoint); err != nil {
			return problem("invalid_argument", "massd.endpoint must be a ws/wss MASS endpoint")
		}
	}
	if c.TokenFile != "" {
		c.TokenFile = resolve(c.TokenFile)
		b, err := readPrivate(c.TokenFile)
		if err != nil {
			return err
		}
		c.token = strings.TrimSpace(string(b))
		if c.token == "" {
			return problem("invalid_argument", "empty massd tokenFile")
		}
	}
	return nil
}

func (c *EmbeddedMassdConfig) validate() error {
	if !filepath.IsAbs(c.DataDir) {
		return problem("invalid_argument", "massd.embedded.dataDir is required")
	}
	if c.MetaStore.Backend != "" && c.MetaStore.Backend != "sqlite" && c.MetaStore.Backend != "file" {
		return problem("invalid_argument", "massd.embedded.metaStore.backend must be sqlite or file")
	}
	if c.SessionIdleTimeout != nil && *c.SessionIdleTimeout < 0 {
		return problem("invalid_argument", "massd.embedded.sessionIdleTimeout must not be negative")
	}
	if c.WriteTimeout != nil && *c.WriteTimeout <= 0 {
		return problem("invalid_argument", "massd.embedded.writeTimeout must be positive")
	}
	return nil
}

func (c Config) massdExposureToken() (string, error) {
	if c.Massd == nil || c.Massd.Embedded == nil || c.Massd.Embedded.Expose == nil {
		return "", nil
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return "", problem("invalid_argument", "massd.embedded.expose requires a loopback Core listener")
	}
	path := c.Massd.Embedded.Expose.TokenFile
	if path == "" {
		return "", problem("invalid_argument", "massd.embedded.expose.tokenFile is required")
	}
	b, err := readPrivate(path)
	if err != nil {
		return "", problem("invalid_argument", "massd.embedded.expose.tokenFile must be a readable private regular file")
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", problem("invalid_argument", "embedded MASS token must have at least 32 bytes without whitespace or control characters")
	}
	// Core assigns this prefix to both new and persisted role MCP credentials.
	if token == c.operatorToken || strings.HasPrefix(token, roleTokenPrefix+"_") {
		return "", problem("invalid_argument", "embedded MASS token must be distinct from Core credentials")
	}
	for _, source := range c.Sources {
		if token == source.token {
			return "", problem("invalid_argument", "embedded MASS token must be distinct from Core credentials")
		}
	}
	return token, nil
}

func (c *EmbeddedMassdConfig) start(ctx context.Context, options TelemetryOptions, token string) (*massd.Instance, error) {
	idle, write := service.DefaultSessionIdleTimeout, massd.DefaultWriteTimeout
	if c.SessionIdleTimeout != nil {
		idle = *c.SessionIdleTimeout
	}
	if c.WriteTimeout != nil {
		write = *c.WriteTimeout
	}
	wire := transport.Options{WriteTimeout: write, TracerProvider: options.Traces, MeterProvider: options.Metrics}
	if c.Expose != nil {
		wire.Token = token
		wire.AllowedOrigins = c.Expose.AllowedOrigins
	}
	return massd.Start(ctx, massd.Options{
		DataDir: c.DataDir, MetaStore: c.MetaStore, SessionIdleTimeout: idle,
		PrototypeAutoApprovePermissions: c.PrototypeAutoApprovePermissions,
		Transport:                       wire,
	})
}

func (s *Server) massClient() (*client.Client, error) {
	options := client.Options{WriteTimeout: 5 * time.Second, SubscriptionQueueCapacity: 32}
	if s.embeddedMassd != nil {
		return client.NewInProcess(s.embeddedMassd.Handler().Connect, options)
	}
	return client.New(s.config.Massd.Endpoint, s.config.Massd.token, options)
}

// Prepare the Core directory and check for overlap before opening either store.
func validateMassdDirectories(coreDir, massdDir string) error {
	coreDir, err := resolveDataPath(coreDir)
	if err != nil {
		return problem("invalid_argument", "cannot resolve Core data directory")
	}
	massdDir, err = resolveDataPath(massdDir)
	if err != nil {
		return problem("invalid_argument", "cannot resolve MASS data directory")
	}
	if err := checkDataDirectoryOverlap(coreDir, massdDir); err != nil {
		return err
	}
	if _, err := os.Stat(coreDir); err == nil {
		return nil
	}
	// Give missing components a filesystem identity. This also exposes aliases
	// that could not be compared before creation, including case variants.
	if err := os.MkdirAll(coreDir, 0o700); err != nil {
		return problem("unavailable", "cannot create data directory")
	}
	return checkDataDirectoryOverlap(coreDir, massdDir)
}

func checkDataDirectoryOverlap(coreDir, massdDir string) error {
	for _, pair := range [][2]string{{coreDir, massdDir}, {massdDir, coreDir}} {
		overlaps, err := directoryContainsPath(pair[0], pair[1])
		if err != nil {
			return problem("invalid_argument", "cannot inspect Core and MASS data directories")
		}
		if overlaps {
			return problem("invalid_argument", "Core and MASS require separate data directories")
		}
	}
	return nil
}

func directoryContainsPath(dir, path string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for {
		if path == dir {
			return true, nil
		}
		if info != nil {
			ancestor, err := os.Stat(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			if os.SameFile(info, ancestor) {
				return true, nil
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

func resolveDataPath(name string) (string, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(name)
	if !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	if _, statErr := os.Lstat(name); !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	parent, err := resolveDataPath(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}
