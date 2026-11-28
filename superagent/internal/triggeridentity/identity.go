package triggeridentity

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SourceHeader    = "X-Superagent-Source"
	AccountHeader   = "X-Superagent-Account-Id"
	InstancePattern = `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`
)

var instanceID = regexp.MustCompile(InstancePattern)

func ReservedSource(value string) bool {
	return strings.HasPrefix(value, "core:")
}

func ValidSource(value string) bool {
	return ValidAccount(value) && !strings.ContainsAny(value, ":/%") &&
		!strings.ContainsFunc(value, unicode.IsSpace)
}

func ValidAccount(value string) bool {
	return value != "" && len(value) <= 2048 && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.Contains(value, ",") &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func ValidInstance(value string) bool {
	return instanceID.MatchString(value)
}

func ValidToken(token string) bool {
	return len(token) >= 32 && !strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}
