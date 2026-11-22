package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

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
}

func (c *MassdConfig) validate(resolve func(string) string, composed bool) error {
	if c.Embedded != nil {
		if composed || c.Endpoint != "" || c.TokenFile != "" {
			return problem("invalid_argument", "massd.embedded cannot be combined with compose, massd.endpoint or massd.tokenFile")
		}
		c.Embedded.DataDir = resolve(c.Embedded.DataDir)
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

func (c *EmbeddedMassdConfig) start(ctx context.Context, options TelemetryOptions) (*massd.Instance, error) {
	idle, write := service.DefaultSessionIdleTimeout, massd.DefaultWriteTimeout
	if c.SessionIdleTimeout != nil {
		idle = *c.SessionIdleTimeout
	}
	if c.WriteTimeout != nil {
		write = *c.WriteTimeout
	}
	return massd.Start(ctx, massd.Options{
		DataDir: c.DataDir, MetaStore: c.MetaStore, SessionIdleTimeout: idle,
		PrototypeAutoApprovePermissions: c.PrototypeAutoApprovePermissions,
		Transport:                       transport.Options{WriteTimeout: write, TracerProvider: options.Traces, MeterProvider: options.Metrics},
	})
}

func (s *Server) massClient() (*client.Client, error) {
	options := client.Options{WriteTimeout: 5 * time.Second, SubscriptionQueueCapacity: 32}
	if s.embeddedMassd != nil {
		return client.NewInProcess(s.embeddedMassd.Handler().Connect, options)
	}
	return client.New(s.config.Massd.Endpoint, s.config.Massd.token, options)
}

// Check both directories before either store creates its files. Resolve the
// existing prefix even when the last components have yet to be created.
func validateMassdDirectories(coreDir, massdDir string) error {
	coreDir, err := resolveDataPath(coreDir)
	if err != nil {
		return problem("invalid_argument", "cannot resolve Core data directory")
	}
	massdDir, err = resolveDataPath(massdDir)
	if err != nil {
		return problem("invalid_argument", "cannot resolve MASS data directory")
	}
	sep := string(filepath.Separator)
	if coreDir == massdDir ||
		strings.HasPrefix(coreDir, strings.TrimSuffix(massdDir, sep)+sep) ||
		strings.HasPrefix(massdDir, strings.TrimSuffix(coreDir, sep)+sep) {
		return problem("invalid_argument", "Core and MASS require separate data directories")
	}
	return nil
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
