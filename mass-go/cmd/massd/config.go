package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func readOptions(cmd *cobra.Command, path string) (options, error) {
	config := viper.New()
	config.SetConfigFile(path)
	config.SetConfigType("yaml")
	if err := config.ReadInConfig(); err != nil {
		if !errors.Is(err, os.ErrNotExist) || cmd.Flags().Changed("config") {
			// Parser errors may include configuration values; do not expose them.
			return options{}, errors.New("cannot read configuration as YAML")
		}
	}
	if err := validateSessionIdleTimeoutType(config); err != nil {
		return options{}, err
	}
	// Validate the file before flags can hide invalid field values.
	if _, err := decodeOptions(config); err != nil {
		return options{}, err
	}
	for key, flag := range map[string]string{
		"data":                            "data",
		"listen":                          "listen",
		"local":                           "local",
		"tokenFile":                       "token-file",
		"tlsCert":                         "tls-cert",
		"tlsKey":                          "tls-key",
		"prototypeAutoApprovePermissions": "prototype-auto-approve-permissions",
		"sessionIdleTimeout":              "session-idle-timeout",
	} {
		if err := config.BindPFlag(key, cmd.Flags().Lookup(flag)); err != nil {
			return options{}, fmt.Errorf("cannot bind configuration flag: %w", err)
		}
	}
	return decodeOptions(config)
}

func validateSessionIdleTimeoutType(config *viper.Viper) error {
	for key, raw := range config.AllSettings() {
		if !strings.EqualFold(key, "sessionIdleTimeout") {
			continue
		}
		if _, ok := raw.(string); ok {
			return nil
		}
		value := reflect.ValueOf(raw)
		switch value.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			if value.IsZero() {
				return nil
			}
		}
		return errors.New("session idle timeout must be a duration string")
	}
	return nil
}

func decodeOptions(config *viper.Viper) (options, error) {
	if fields, ok := config.Get("metaStore").(map[string]any); ok {
		if raw, present := fields["backend"]; present {
			name, valid := raw.(string)
			if !valid || (name != "sqlite" && name != "file") {
				return options{}, errors.New("MetaStore backend must be sqlite or file")
			}
		}
	}

	var configured options
	if err := config.UnmarshalExact(&configured, func(c *mapstructure.DecoderConfig) {
		c.WeaklyTypedInput = false
	}); err != nil {
		return options{}, errors.New("configuration must contain known fields with valid types")
	}
	switch configured.MetaStore.Backend {
	case "", "sqlite", "file":
	default:
		return options{}, errors.New("unknown MetaStore backend: select sqlite or file")
	}
	if configured.SessionIdleTimeout < 0 {
		return options{}, errors.New("session idle timeout must not be negative")
	}
	return configured, nil
}
