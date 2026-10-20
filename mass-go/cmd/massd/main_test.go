package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type commandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(commandSuite)) }

func (s *commandSuite) TestParsing() {
	for _, args := range [][]string{{"--help"}, {"--unknown"}, {"--listen"}, {"--local=invalid"}, {"extra"}, {"--local", "--listen", "0.0.0.0:0"}} {
		s.Run(strings.Join(args, " "), func() {
			cmd := newCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(s.T().Context())
			if args[0] == "--help" {
				s.Require().NoError(err)
				s.Contains(out.String(), "--token-file")
				s.Contains(out.String(), "--local")
			} else {
				s.Require().Error(err)
			}
		})
	}
}
