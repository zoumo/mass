package core

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"

	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/service"
	"github.com/zoumo/mass/massd/pkg/transport"
)

const embeddedMassdDir = "massd"

type MassdConfig struct {
	Endpoint  string `mapstructure:"endpoint"`
	TokenFile string `mapstructure:"tokenFile"`
	token     string
}

func takeMassdConfig(fields map[string]any) (*MassdConfig, error) {
	var config *MassdConfig
	for name, raw := range fields {
		if !strings.EqualFold(name, "massd") {
			continue
		}
		if config != nil {
			return nil, problem("invalid_argument", "configuration contains duplicate massd fields")
		}
		mapping, ok := raw.(map[string]any)
		if !ok {
			return nil, problem("invalid_argument", "massd must be a mapping with an endpoint")
		}
		seen := make(map[string]bool, len(mapping))
		for field := range mapping {
			key := strings.ToLower(field)
			if seen[key] {
				return nil, problem("invalid_argument", "massd configuration contains duplicate fields")
			}
			seen[key] = true
		}
		config = &MassdConfig{}
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{ErrorUnused: true, Result: config})
		if err != nil {
			return nil, err
		}
		if err := decoder.Decode(raw); err != nil {
			return nil, problem("invalid_argument", "massd configuration contains unknown or invalid fields")
		}
		delete(fields, name)
	}
	return config, nil
}

func (c *MassdConfig) validateEndpoint() error {
	if _, err := client.WebSocketEndpoint(c.Endpoint); err != nil {
		return problem("invalid_argument", "massd.endpoint must be a ws/wss MASS endpoint")
	}
	return nil
}

func (c *MassdConfig) loadToken() error {
	if c.TokenFile != "" {
		b, err := readPrivate(c.TokenFile)
		if err != nil {
			return problem("invalid_argument", "massd.tokenFile must be a readable private regular file")
		}
		c.token = strings.TrimSpace(string(b))
		if c.token == "" {
			return problem("invalid_argument", "empty massd tokenFile")
		}
	}
	return nil
}

func startEmbeddedMassd(ctx context.Context, dataDir string, options TelemetryOptions, adminToken string) (*massd.Instance, error) {
	return massd.Start(ctx, massd.Options{
		DataDir:            filepath.Join(dataDir, embeddedMassdDir),
		SessionIdleTimeout: service.DefaultSessionIdleTimeout,
		Transport: transport.Options{
			Token: adminToken, WriteTimeout: massd.DefaultWriteTimeout,
			TracerProvider: options.Traces, MeterProvider: options.Metrics,
		},
	})
}

func (s *Server) massClient() (*client.Client, error) {
	options := client.Options{WriteTimeout: 5 * time.Second, SubscriptionQueueCapacity: 32}
	if s.embeddedMassd != nil {
		return client.NewInProcess(s.embeddedMassd.Handler().Connect, options)
	}
	return client.New(s.config.Massd.Endpoint, s.config.Massd.token, options)
}
