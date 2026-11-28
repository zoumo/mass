package trigger

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/zoumo/mass/internal/httpaddress"
)

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func ValidIdentifier(value string) bool { return instanceIDPattern.MatchString(value) }

func ValidateCoreEndpoint(s string) error {
	return validateHTTPEndpoint(s, "coreEndpoint")
}

func ValidateStatusEndpoint(s string) error {
	return validateHTTPEndpoint(s, "status endpoint")
}

func validateHTTPEndpoint(s, name string) error {
	if _, err := httpaddress.ParseOrigin(s); err != nil {
		return fmt.Errorf("%s %w", name, err)
	}
	return nil
}

func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func ResolveSectionPath(configPath, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}

func ValidateStatusListen(address string) error {
	if _, _, err := httpaddress.SplitListen(address); err != nil {
		return fmt.Errorf("statusListen %w", err)
	}
	return nil
}

// Recording a diagnostic must never prevent processing or acknowledging an event.
func WriteDiagnostic(enc *json.Encoder, data any) {
	_ = enc.Encode(data) //nolint:errchkjson
}
