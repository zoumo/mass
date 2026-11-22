package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type commandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(commandSuite)) }

func (s *commandSuite) TestParsing() {
	s.T().Setenv("HOME", s.T().TempDir())
	for _, args := range [][]string{{"--help"}, {"--unknown"}, {"--listen"}, {"--local"}, {"extra"}} {
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
				s.NotContains(out.String(), "--mcp-files-dir")
				s.NotContains(out.String(), "--local")
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
	s.Require().NoError(os.WriteFile(path, []byte("# daemon settings\nlisten: 127.0.0.1:23456\n"), 0o600))
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal("127.0.0.1:23456", got.Listen)
	s.Equal(defaults.Data, got.Data)
	s.Require().NoError(cmd.Flags().Set("listen", defaults.Listen))
	got, err = readOptions(cmd, path)
	s.Require().NoError(err)
	s.Equal(defaults, got, "explicit flags override file values")
	for _, invalid := range []string{`[]`, `unknown: true`, `local: true`, `local: false`, `listen: 123`, "listen: 127.0.0.1:0\nlisten: 127.0.0.1:1", `{`} {
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
		Data: "/owned/data", Listen: "127.0.0.1:23456",
		TokenFile: "/owned/token", TLSCert: "/owned/cert", TLSKey: "/owned/key", Approve: true,
		AllowedOrigins:     []string{"https://allowed.example"},
		SessionIdleTimeout: 10 * time.Minute,
		WriteTimeout:       7 * time.Second,
	}, got)
	for name, value := range map[string]string{
		"data": "/other/data", "listen": "127.0.0.1:0",
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

func (s *commandSuite) TestDataDirConfiguration() {
	home := s.T().TempDir()
	s.T().Setenv("HOME", home)
	s.T().Chdir(home)
	configDir := filepath.Join(home, "run")
	s.Require().NoError(os.Mkdir(configDir, 0o700))
	path := filepath.Join("run", "massd.yaml")
	absolute := filepath.Join(home, "explicit")
	for _, tc := range []struct {
		name, yaml, flag, data, errorMessage string
	}{
		{name: "relative", yaml: "dataDir: ./massd", data: filepath.Join(configDir, "massd")},
		{name: "absolute", yaml: "dataDir: " + absolute, data: absolute},
		{name: "default", yaml: "{}", data: filepath.Join(home, ".mass")},
		{name: "legacy", yaml: "data: " + absolute, data: absolute},
		{name: "legacy relative remains unresolved", yaml: "data: ./massd", data: "./massd"},
		{name: "explicit flag", yaml: "dataDir: ./massd", flag: absolute, data: absolute},
		{name: "conflicting fields", yaml: "dataDir: ./massd\ndata: " + absolute, flag: absolute, errorMessage: "choose only one of dataDir or data"},
		{name: "null legacy field still conflicts", yaml: "dataDir: ./massd\ndata: null", errorMessage: "choose only one of dataDir or data"},
		{name: "null dataDir still conflicts", yaml: "dataDir: null\ndata: " + absolute, errorMessage: "choose only one of dataDir or data"},
		{name: "invalid type even with flag", yaml: "dataDir: 72", flag: absolute, errorMessage: "known fields with valid types"},
	} {
		s.Run(tc.name, func() {
			s.Require().NoError(os.WriteFile(path, []byte(tc.yaml+"\n"), 0o600))
			cmd := newCommand()
			if tc.flag != "" {
				s.Require().NoError(cmd.Flags().Set("data", tc.flag))
			}
			got, err := readOptions(cmd, path)
			if tc.errorMessage != "" {
				s.Require().ErrorContains(err, tc.errorMessage)
				return
			}
			s.Require().NoError(err)
			s.Equal(tc.data, got.Data)
		})
	}
}
