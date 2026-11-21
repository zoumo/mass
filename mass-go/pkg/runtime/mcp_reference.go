package runtime

import (
	"errors"
	"fmt"
	"regexp"
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

var mcpEnvironmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolveMCPEnvironmentReferences substitutes the explicit ${NAME} references
// in a declared value in one pass. Replacement values are opaque.
func ResolveMCPEnvironmentReferences(value string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	matches := mcpEnvironmentReference.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return "", errors.New("contains an unresolved environment reference")
	}
	var resolved strings.Builder
	last := 0
	for _, match := range matches {
		if strings.Contains(value[last:match[0]], "${") {
			return "", errors.New("contains an unresolved environment reference")
		}
		resolved.WriteString(value[last:match[0]])
		replacement, err := LookupMCPEnvironmentReference(lookup, value[match[2]:match[3]])
		if err != nil {
			return "", err
		}
		resolved.WriteString(replacement)
		last = match[1]
	}
	if strings.Contains(value[last:], "${") {
		return "", errors.New("contains an unresolved environment reference")
	}
	resolved.WriteString(value[last:])
	return resolved.String(), nil
}

// ValidMCPHeaderValue rejects characters that cannot appear in HTTP fields.
func ValidMCPHeaderValue(value string) bool {
	return strings.IndexFunc(value, func(c rune) bool {
		return c < ' ' && c != '\t' || c == 0x7f
	}) == -1
}
