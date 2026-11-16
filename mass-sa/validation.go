package sa

import (
	"strings"
	"unicode/utf8"
)

func validateSingleLine(label, value string, required bool) error {
	if err := validateTextContent(label, value); err != nil {
		return err
	}
	if required && strings.TrimSpace(value) == "" {
		return operationError("invalid_argument", "invalid_argument", label+" is required")
	}
	if strings.ContainsAny(value, "\r\n") {
		return operationError("invalid_argument", "invalid_argument", label+" must be a single line")
	}
	return nil
}

func validateBlockText(label, value string, required bool) error {
	if err := validateTextContent(label, value); err != nil {
		return err
	}
	if required && strings.TrimSpace(value) == "" {
		return operationError("invalid_argument", "invalid_argument", label+" is required")
	}
	return nil
}

func validateTextContent(label, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return operationError("invalid_argument", "invalid_text", label+" must be valid UTF-8 without NUL")
	}
	return nil
}
