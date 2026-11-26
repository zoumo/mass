package cua

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayURL(t *testing.T) {
	for _, origin := range []string{"https://relay.invalid:5943/", "http://127.0.0.1:3211", "http://[::1]:3211", "http://localhost"} {
		relay, err := parseRelayURL(origin)
		require.NoError(t, err, origin)
		assert.Equal(t, "/m/first-mac", relay.JoinPath("m", "first-mac").EscapedPath())
	}
	for _, origin := range []string{
		"http://relay.invalid", "file:///tmp/cua", "https://name@relay.invalid", "https://relay.invalid/m/a",
		"https://relay.invalid/%2f", "https://relay.invalid?secret", "https://relay.invalid#", "https:/relay.invalid",
	} {
		_, err := parseRelayURL(origin)
		assert.Error(t, err, origin)
	}
}

func TestCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.token")
	require.NoError(t, os.WriteFile(path, []byte("fixture-access-token\n"), 0o600))
	token, err := readToken(path)
	require.NoError(t, err)
	assert.Equal(t, "fixture-access-token", token)

	symlink := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, symlink))
	_, err = readToken(symlink)
	require.ErrorContains(t, err, "cannot open")
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = readToken(path)
	require.ErrorContains(t, err, "owner-only permissions")
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("fixture-access-token\nsecond-line"), 0o600))
	_, err = readToken(path)
	require.ErrorContains(t, err, "one non-empty token")
}

func TestDriverConfigUsesAbsoluteTargetPaths(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "access.token"), []byte("fixture-access-token\n"), 0o600))
	configFile := filepath.Join(dir, "cua-mcp.yaml")
	const base = `
cuaCLI: ./cua
relayURL: https://relay.invalid
computers:
  - id: first-mac
    tokenFile: access.token
`
	for _, tc := range []struct {
		name   string
		driver string
	}{
		{"relative CLI", "    driver: {cli: ./cua-driver, socket: /mac/driver.sock}\n"},
		{"relative socket", "    driver: {cli: /mac/cua-driver, socket: driver.sock}\n"},
		{"missing socket", "    driver: {cli: /mac/cua-driver}\n"},
		{"empty driver", "    driver: {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(configFile, []byte(base+tc.driver), 0o600))
			_, err := loadConfig(configFile)
			require.ErrorContains(t, err, "driver.cli and driver.socket must be absolute paths on the target Mac")
		})
	}
	require.NoError(t, os.WriteFile(configFile, []byte(base+"    driver: {cli: /mac/cua-driver, socket: /mac/driver.sock}\n"), 0o600))
	cfg, err := loadConfig(configFile)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "cua"), cfg.cuaCLI)
	assert.Equal(t, &driverConfig{CLI: "/mac/cua-driver", Socket: "/mac/driver.sock"}, cfg.computers["first-mac"].driver)
}
