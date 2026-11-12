package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type commandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(commandSuite)) }

type closeCounter struct {
	closed int
	err    error
}

type delayedShutdown struct{}

func (*delayedShutdown) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *closeCounter) Close() error {
	c.closed++
	return c.err
}

func (s *commandSuite) TestStoreOwnershipRetainedForUnsettledService() {
	first, second := &closeCounter{}, &closeCounter{}
	settled, err := settleService(&delayedShutdown{}, 20*time.Millisecond)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.False(settled)
	s.Require().NoError(closeStores(settled, first, second))
	s.Zero(first.closed, "Store ownership must remain held while settlement is active")
	s.Zero(second.closed)

	s.Require().NoError(closeStores(true, first, second))
	s.Equal(1, first.closed)
	s.Equal(1, second.closed)

	first.err = errors.New("first close failed")
	second.err = errors.New("second close failed")
	err = closeStores(true, first, second)
	s.Require().ErrorContains(err, "first close failed")
	s.Require().ErrorContains(err, "second close failed")
}

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
				s.Contains(out.String(), "--session-idle-timeout")
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
	defaults := options{Listen: "127.0.0.1:24368", Data: filepath.Join(home, ".mass"), AllowedOrigins: []string{}, SessionIdleTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Second}
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
allowedOrigins:
  - https://allowed.example
prototypeAutoApprovePermissions: true
writeTimeout: 7s
`), 0o600))
	cmd := newCommand()
	got, err := readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(options{
		Data: "/owned/data", Listen: "127.0.0.1:23456", Local: true,
		TokenFile: "/owned/token", TLSCert: "/owned/cert", TLSKey: "/owned/key", Approve: true,
		AllowedOrigins:     []string{"https://allowed.example"},
		SessionIdleTimeout: 10 * time.Minute,
		WriteTimeout:       7 * time.Second,
	}, got)
	for name, value := range map[string]string{
		"data": "/other/data", "listen": "127.0.0.1:0", "local": "false",
		"token-file": "", "tls-cert": "/other/cert", "tls-key": "/other/key",
		"allowed-origin":                     "https://flag.example",
		"prototype-auto-approve-permissions": "false",
		"write-timeout":                      "3s",
	} {
		s.Require().NoError(cmd.Flags().Set(name, value))
	}
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(options{
		Data: "/other/data", Listen: "127.0.0.1:0",
		TLSCert: "/other/cert", TLSKey: "/other/key",
		AllowedOrigins:     []string{"https://flag.example"},
		SessionIdleTimeout: 10 * time.Minute,
		WriteTimeout:       3 * time.Second,
	}, got, "explicit empty strings and false values must override the file")

	got, err = readOptions(newCommand(), filepath.Join(home, ".mass", "config.yaml"))
	s.Require().NoError(err)
	s.Equal(options{Data: filepath.Join(home, ".mass"), Listen: "127.0.0.1:24368", AllowedOrigins: []string{}, SessionIdleTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Second}, got,
		"configuration and flags must not leak between command instances")
}

func (s *commandSuite) TestMetadataConfiguration() {
	path := filepath.Join(s.T().TempDir(), "daemon.yaml")
	for _, backend := range []string{"sqlite", "file"} {
		s.Require().NoError(os.WriteFile(path, []byte("metaStore:\n  backend: "+backend+"\n"), 0o600))
		got, err := readOptions(newCommand(), path)
		s.Require().NoError(err)
		s.Equal(backend, got.MetaStore.Backend)
	}
	for _, value := range []string{"bogus", "123", "true", "[]", `""`, "null"} {
		s.Require().NoError(os.WriteFile(path, []byte("metaStore:\n  backend: "+value+"\n"), 0o600))
		_, err := readOptions(newCommand(), path)
		s.Require().Error(err, value)
	}
}
