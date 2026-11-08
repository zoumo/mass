package runtime

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// LookupMCPEnvironmentReference returns a usable environment value for name.
// lookup must reflect the environment passed to the provider process. Missing,
// empty and invalid values fail without exposing the value.
func LookupMCPEnvironmentReference(lookup func(string) (string, bool), name string) (string, error) {
	value, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("environment variable %q is unavailable", name)
	}
	if value == "" {
		return "", fmt.Errorf("environment variable %q is empty", name)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("environment variable %q has an invalid value", name)
	}
	return value, nil
}
