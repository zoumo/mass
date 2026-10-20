package main

import (
	"errors"
	"fmt"
	"os"

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
	} {
		if err := config.BindPFlag(key, cmd.Flags().Lookup(flag)); err != nil {
			return options{}, fmt.Errorf("cannot bind configuration flag: %w", err)
		}
	}
	return decodeOptions(config)
}

func decodeOptions(config *viper.Viper) (options, error) {
	var configured options
	if err := config.UnmarshalExact(&configured, func(c *mapstructure.DecoderConfig) {
		c.WeaklyTypedInput = false
	}); err != nil {
		return options{}, errors.New("configuration must contain known fields with valid types")
	}
	return configured, nil
}
