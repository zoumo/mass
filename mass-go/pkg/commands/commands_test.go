package commands_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/commands"
)

type commandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(commandSuite)) }

func (s *commandSuite) TestHelpAndInvalidArguments() {
	for _, args := range [][]string{{"--help"}, nil, {"--unknown"}, {"--op"}, {"extra"}, {"--op", "runtime.list", "extra"}} {
		s.Run(strings.Join(args, " "), func() {
			var out, stderr bytes.Buffer
			err := commands.Run(s.T().Context(), args, strings.NewReader("{}"), &out, &stderr)
			if len(args) == 1 && args[0] == "--help" {
				s.Require().NoError(err)
				s.Contains(out.String(), "--op")
				s.Contains(out.String(), "--url")
			} else {
				s.Require().Error(err)
				s.Empty(out.String())
			}
		})
	}
}

func (s *commandSuite) TestFlagsAndIO() {
	s.T().Setenv("MASS_TOKEN", "command-test-token")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		s.Equal(http.MethodGet, r.Method)
		s.Equal("/v1/runtimes", r.URL.Path)
		s.Equal("Bearer command-test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	for range 2 {
		var out, stderr bytes.Buffer
		s.Require().NoError(commands.Run(s.T().Context(), []string{"--url=" + server.URL, "--op", "runtime.list"}, strings.NewReader("{}"), &out, &stderr))
		s.JSONEq("[]", out.String())
		s.Empty(stderr.String())
	}
	s.Equal(2, requests, "each invocation has independent flag state")
}
