package trigger

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func ValidIdentifier(value string) bool { return instanceIDPattern.MatchString(value) }

func parseOrigin(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		strings.Contains(s, "#") || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return nil, errors.New("endpoint must be a URL origin without credentials, path, query or fragment")
	}
	return u, nil
}

func ValidateCoreEndpoint(s string, allowInsecureHTTP bool) error {
	u, err := parseOrigin(s)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if net.ParseIP(u.Hostname()).IsLoopback() {
			if u.Port() == "" {
				return errors.New("loopback coreEndpoint requires a port")
			}
			return nil
		}
		if allowInsecureHTTP {
			return nil
		}
		return errors.New("remote coreEndpoint requires HTTPS or allowInsecureCoreHTTP: true")
	default:
		return errors.New("coreEndpoint must use HTTP or HTTPS")
	}
}

func ValidateStatusEndpoint(s string) error {
	u, err := parseOrigin(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" {
		return errors.New("status endpoint must be an HTTP numeric loopback origin with port")
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
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("statusListen must be numeric loopback")
	}
	return nil
}

// Recording a diagnostic must never prevent processing or acknowledging an event.
func WriteDiagnostic(enc *json.Encoder, data any) {
	_ = enc.Encode(data) //nolint:errchkjson
}
