// Package httpaddress validates the network addresses used by Superagent HTTP services.
package httpaddress

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var dnsLabel = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validHost(host string, bracketed bool) bool {
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Is6() == bracketed
	}
	host = strings.TrimSuffix(host, ".")
	if bracketed || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// ValidAuthority accepts a DNS name, IPv4, or bracketed IPv6, with an optional port.
func ValidAuthority(authority string) bool {
	bracketed := strings.HasPrefix(authority, "[")
	switch {
	case bracketed && strings.HasSuffix(authority, "]"):
		return validHost(authority[1:len(authority)-1], true)
	case strings.Contains(authority, ":"):
		host, port, err := net.SplitHostPort(authority)
		number, portErr := strconv.ParseUint(port, 10, 16)
		return err == nil && portErr == nil && number != 0 && validHost(host, bracketed)
	default:
		return validHost(authority, false)
	}
}

// SplitListen validates a TCP bind address. An empty host and port zero denote
// a wildcard and an OS-assigned port, respectively.
func SplitListen(address string) (string, uint16, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || (!strings.HasPrefix(address, ":") && !validHost(host, strings.HasPrefix(address, "["))) {
		return "", 0, errors.New("must use host:port with a numeric port")
	}
	return host, uint16(number), nil
}

// ParseBaseURL accepts an HTTP base URL with an optional path prefix.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !ValidAuthority(u.Host) ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return nil, errors.New("must be an HTTP or HTTPS base URL without credentials, a query, or a fragment")
	}
	return u, nil
}

// ParseOrigin accepts an HTTP base URL at the root path.
func ParseOrigin(raw string) (*url.URL, error) {
	u, err := ParseBaseURL(raw)
	if err != nil || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return nil, errors.New("must be an HTTP or HTTPS origin without credentials, a path, a query, or a fragment")
	}
	u.Path = ""
	return u, nil
}
