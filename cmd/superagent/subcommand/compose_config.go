package subcommand

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/superagent/triggers"
)

type composeConfig struct {
	core             []byte
	triggers         map[string][]byte
	watchdogInterval time.Duration
}

func loadComposeConfig(path string) (composeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return composeConfig{}, errors.New("cannot open Compose configuration")
	}
	defer file.Close()
	return decodeComposeConfig(file)
}

func decodeComposeConfig(input io.Reader) (composeConfig, error) {
	var config composeConfig
	decoder := yaml.NewDecoder(input)
	var root, extra yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return config, errors.New("cannot read Compose configuration as YAML")
	}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return config, errors.New("compose configuration must contain one YAML document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return config, errors.New("compose configuration must be a mapping")
	}
	document := root.Content[0]
	if err := checkMappingKeys(document, "compose configuration"); err != nil {
		return config, err
	}
	for i := 0; i < len(document.Content); i += 2 {
		key, value := document.Content[i], document.Content[i+1]
		switch key.Value {
		case "core":
			var err error
			config.core, err = encodeComposeSection(value, "core configuration")
			if err != nil {
				return config, err
			}
		case "triggers":
			var err error
			config.triggers, err = decodeComposeTriggers(value)
			if err != nil {
				return config, err
			}
		case "watchdog":
			var err error
			config.watchdogInterval, err = decodeComposeWatchdog(value)
			if err != nil {
				return config, err
			}
		default:
			return config, fmt.Errorf("unknown Compose configuration section at line %d", key.Line)
		}
	}
	if config.core == nil {
		return config, errors.New("missing core configuration")
	}
	return config, nil
}

func decodeComposeWatchdog(node *yaml.Node) (time.Duration, error) {
	if node.Kind != yaml.MappingNode {
		return 0, errors.New("watchdog configuration must be a mapping")
	}
	if err := checkMappingKeys(node, "watchdog configuration"); err != nil {
		return 0, err
	}
	var interval time.Duration
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value != "interval" {
			return 0, fmt.Errorf("unknown watchdog configuration field at line %d", key.Line)
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return 0, errors.New("watchdog.interval must be a positive duration string")
		}
		interval, _ = time.ParseDuration(value.Value)
	}
	if interval <= 0 {
		return 0, errors.New("watchdog.interval must be a positive duration string")
	}
	return interval, nil
}

func decodeComposeTriggers(node *yaml.Node) (map[string][]byte, error) {
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("triggers configuration must be a mapping")
	}
	if err := checkMappingKeys(node, "triggers configuration"); err != nil {
		return nil, err
	}
	sections := make(map[string][]byte, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value != triggers.GitHub && key.Value != triggers.DWS {
			return nil, fmt.Errorf("unknown triggers configuration section at line %d", key.Line)
		}
		data, err := encodeComposeSection(value, "triggers."+key.Value+" configuration")
		if err != nil {
			return nil, err
		}
		sections[key.Value] = data
	}
	return sections, nil
}

func encodeComposeSection(node *yaml.Node, section string) ([]byte, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", section)
	}
	if err := checkComposeKeys(node, section); err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := node.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("cannot read %s", section)
	}
	data, err := yaml.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", section)
	}
	return data, nil
}

func checkComposeKeys(node *yaml.Node, section string) error {
	if node.Kind == yaml.MappingNode {
		if err := checkMappingKeys(node, section); err != nil {
			return err
		}
	}
	for _, child := range node.Content {
		if err := checkComposeKeys(child, section); err != nil {
			return err
		}
	}
	return nil
}

func checkMappingKeys(node *yaml.Node, section string) error {
	keys := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return fmt.Errorf("%s requires string keys at line %d", section, key.Line)
		}
		// Component loaders treat field names case-insensitively.
		name := strings.ToLower(key.Value)
		if keys[name] {
			return fmt.Errorf("%s contains a duplicate key at line %d", section, key.Line)
		}
		keys[name] = true
	}
	return nil
}
