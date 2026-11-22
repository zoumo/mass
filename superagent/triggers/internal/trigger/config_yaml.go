package trigger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

var yamlErrorLine = regexp.MustCompile(`^(?:yaml: (?:unmarshal errors:\s*)?)?line (\d+):`)

func DecodeYAML(path string, section io.Reader, destination any) error {
	if section == nil {
		file, err := os.Open(path)
		if err != nil {
			return errors.New("cannot open configuration")
		}
		defer file.Close()
		section = file
	}
	decoder := yaml.NewDecoder(section)
	decoder.KnownFields(true)
	if err := decoder.Decode(destination); err != nil {
		// Decoder errors can contain scalar values from the config.
		if details := yamlErrorLine.FindStringSubmatch(err.Error()); details != nil {
			return fmt.Errorf("invalid configuration YAML on line %s", details[1])
		}
		return errors.New("invalid configuration YAML")
	}
	if err := decoder.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return errors.New("configuration must contain a single YAML document")
	}
	return nil
}
