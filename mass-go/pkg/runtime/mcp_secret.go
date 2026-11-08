package runtime

import (
	"regexp"
	"strings"
)

var (
	mcpEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mcpSecretReference = regexp.MustCompile(`^(?:([A-Za-z][A-Za-z0-9_-]*) )?\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// IsSensitiveMCPName reports whether a header or environment name is treated
// as secret-bearing by provider-neutral MCP validation.
func IsSensitiveMCPName(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range []string{"authorization", "cookie", "credential", "password", "secret", "token", "api-key", "apikey"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return strings.HasSuffix(name, "-key") || strings.HasSuffix(name, "_key")
}

// ParseMCPSecretReference splits a validated secret value into its optional
// scheme prefix and environment-variable name. It never evaluates the value.
func ParseMCPSecretReference(value string) (prefix, name string, ok bool) {
	match := mcpSecretReference.FindStringSubmatch(value)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}
