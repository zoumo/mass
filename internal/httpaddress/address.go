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

func isIPv6(host string) bool {
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Is6()
}

// ValidAuthority accepts a DNS name, IPv4, or bracketed IPv6, with an optional port.
func ValidAuthority(authority string) bool {
	host, port, valid := splitAuthority(authority)
	if len(port) > 1 && port[0] == '0' {
		return false
	}
	if !valid || strings.HasPrefix(authority, "[") {
		return valid
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func splitAuthority(authority string) (host, port string, valid bool) {
	bracketed := strings.HasPrefix(authority, "[")
	host = authority
	switch {
	case bracketed && strings.HasSuffix(authority, "]"):
		host = authority[1 : len(authority)-1]
	case strings.Contains(authority, ":"):
		var err error
		host, port, err = net.SplitHostPort(authority)
		number, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || portErr != nil || number == 0 {
			return "", "", false
		}
	}
	if bracketed {
		return host, port, isIPv6(host)
	}
	return host, port, host != "" && !strings.ContainsAny(host, "[]")
}

// SplitListen validates a TCP bind address. An empty host and port zero denote
// a wildcard and an OS-assigned port, respectively.
func SplitListen(address string) (string, uint16, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || !validListenHost(host, strings.HasPrefix(address, "[")) {
		return "", 0, errors.New("must use host:port with a numeric port")
	}
	return host, uint16(number), nil
}

func validListenHost(host string, bracketed bool) bool {
	if bracketed {
		return isIPv6(host)
	}
	if host == "" {
		return true
	}
	u, err := url.Parse("http://" + host)
	return err == nil && u.Host == host && !strings.ContainsAny(host, "[]")
}

// ParseBaseURL accepts an HTTP base URL with an optional path prefix.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	const message = "must be an HTTP or HTTPS base URL without credentials, a query, or a fragment"
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return nil, errors.New(message)
	}
	if _, _, valid := splitAuthority(u.Host); !valid {
		return nil, errors.New(message)
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
