package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type commandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(commandSuite)) }

func (s *commandSuite) TestParsing() {
	s.T().Setenv("HOME", s.T().TempDir())
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

func (s *commandSuite) TestConfiguration() {
	home := s.T().TempDir()
	s.T().Setenv("HOME", home)
	path := filepath.Join(home, ".mass", "config.yaml")
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	defaults := options{Listen: "127.0.0.1:24368", Data: filepath.Join(home, ".mass", "data")}
	cmd := newCommand()
	s.Equal(path, cmd.Flags().Lookup("config").DefValue)
	s.Equal(defaults.Listen, cmd.Flags().Lookup("listen").DefValue)
	s.Equal(defaults.Data, cmd.Flags().Lookup("data").DefValue)
	got, err := readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(defaults, got)
	s.Require().NoError(cmd.Flags().Set("config", path))
	_, err = readOptions(cmd, path)
	s.Require().Error(err, "an explicitly selected missing config must fail")
	s.Require().NoError(os.WriteFile(path, []byte("# daemon settings\nlisten: 127.0.0.1:23456\nlocal: true\n"), 0o600))
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal("127.0.0.1:23456", got.Listen)
	s.Equal(defaults.Data, got.Data)
	s.True(got.Local)
	s.Require().NoError(cmd.Flags().Set("listen", defaults.Listen))
	s.Require().NoError(cmd.Flags().Set("local", "false"))
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(defaults, got, "explicit flags, including false, override file values")
	for _, invalid := range []string{`[]`, `unknown: true`, `local: "true"`, `listen: 123`, "local: true\nlocal: false", `local: [`, `{`} {
		s.Require().NoError(os.WriteFile(path, []byte(invalid), 0o600))
		_, err = readOptions(cmd, path)
		s.Require().Error(err, invalid)
	}
}

func (s *commandSuite) TestConfigurationFieldsAndIsolation() {
	home := s.T().TempDir()
	s.T().Setenv("HOME", home)
	path := filepath.Join(home, "daemon.yaml")
	s.Require().NoError(os.WriteFile(path, []byte(`
data: /owned/data
listen: 127.0.0.1:23456
local: true
tokenFile: /owned/token
tlsCert: /owned/cert
tlsKey: /owned/key
prototypeAutoApprovePermissions: true
`), 0o600))
	cmd := newCommand()
	got, err := readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(options{
		Data: "/owned/data", Listen: "127.0.0.1:23456", Local: true,
		TokenFile: "/owned/token", TLSCert: "/owned/cert", TLSKey: "/owned/key", Approve: true,
	}, got)
	for name, value := range map[string]string{
		"data": "/other/data", "listen": "127.0.0.1:0", "local": "false",
		"token-file": "", "tls-cert": "/other/cert", "tls-key": "/other/key",
		"prototype-auto-approve-permissions": "false",
	} {
		s.Require().NoError(cmd.Flags().Set(name, value))
	}
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(options{
		Data: "/other/data", Listen: "127.0.0.1:0",
		TLSCert: "/other/cert", TLSKey: "/other/key",
	}, got, "explicit empty strings and false values must override the file")

	got, err = readOptions(newCommand(), filepath.Join(home, ".mass", "config.yaml"))
	s.Require().NoError(err)
	s.Equal(options{Data: filepath.Join(home, ".mass", "data"), Listen: "127.0.0.1:24368"}, got,
		"configuration and flags must not leak between command instances")
}
