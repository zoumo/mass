//go:build coretest

package triggers

import (
	"net"
	"net/url"
	"os"
)

func githubEndpoint() string {
	u, err := url.Parse(os.Getenv("TEST_GITHUB_ENDPOINT"))
	if err == nil && u.Scheme == "http" && u.Port() != "" && net.ParseIP(u.Hostname()).IsLoopback() &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" {
		return u.String()
	}
	return githubAPIOrigin
}
