package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/zoumo/mass/superagent/internal/triggeridentity"
)

const (
	credentialsDir   = "credentials"
	adminTokenFile   = "admin.token"
	triggerTokenFile = "triggers.token"
)

type instanceCredentials struct {
	adminToken   string
	triggerToken string
	triggerHash  [32]byte
}

// AdminTokenPath locates the instance admin token without initializing Core.
func (c Config) AdminTokenPath() string {
	return filepath.Join(c.DataDir, credentialsDir, adminTokenFile)
}

func (c Config) credentialsPath() string { return filepath.Join(c.DataDir, credentialsDir) }

// ReadOperatorToken also accepts explicitly provisioned token files for remote calls.
func ReadOperatorToken(path string) (string, error) {
	token, err := readCredential(path, adminTokenFile, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", problem("invalid_argument", "admin.token is missing")
	}
	return token, err
}

// ReadTriggerToken reads Core's credential after a supervised Core reports ready.
func (c Config) ReadTriggerToken() (string, error) {
	credentials, err := c.loadCredentials(false)
	return credentials.triggerToken, err
}

// CheckUpstreamToken keeps a supervised adapter's native credential separate from Core credentials.
func (c Config) CheckUpstreamToken(token, triggerToken string) error {
	admin, err := ReadOperatorToken(c.AdminTokenPath())
	if err != nil {
		return err
	}
	external := MassdConfig{}
	if c.Massd != nil {
		external = *c.Massd
		if err := external.loadToken(); err != nil {
			return err
		}
	}
	if token == triggerToken || token == admin || token == external.token || strings.HasPrefix(token, roleTokenPrefix+"_") {
		return errors.New("native service credentials must be distinct from Core credentials")
	}
	return nil
}

// Core must hold the data directory lock when create is true.
func (c Config) loadCredentials(create bool) (instanceCredentials, error) {
	var credentials instanceCredentials
	dir := c.credentialsPath()
	if create {
		if err := os.Mkdir(dir, 0o700); err == nil {
			if err := os.Chmod(dir, 0o700); err != nil {
				return credentials, problem("unavailable", "cannot set Core credentials directory permissions")
			}
			if err := syncCredentialDirectory(c.DataDir); err != nil {
				return credentials, err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return credentials, problem("unavailable", "cannot create Core credentials directory")
		}
	}
	if err := checkCoreSubdirectory(dir, credentialsDir); err != nil {
		return credentials, err
	}
	externalToken := ""
	if c.Massd != nil {
		externalToken = c.Massd.token
	}
	for _, file := range []struct {
		name  string
		value *string
	}{
		{adminTokenFile, &credentials.adminToken},
		{triggerTokenFile, &credentials.triggerToken},
	} {
		path := filepath.Join(dir, file.name)
		token, err := readCredential(path, file.name, true)
		if create && errors.Is(err, os.ErrNotExist) {
			token, err = createCredential(path)
		}
		if errors.Is(err, os.ErrNotExist) {
			return instanceCredentials{}, problem("invalid_argument", file.name+" is missing")
		}
		if err != nil {
			return instanceCredentials{}, err
		}
		if token == externalToken || strings.HasPrefix(token, roleTokenPrefix+"_") {
			return instanceCredentials{}, problem("invalid_argument", "Core credentials must be distinct from role MCP and external MASS credentials")
		}
		*file.value = token
	}
	if credentials.adminToken == credentials.triggerToken {
		return instanceCredentials{}, problem("invalid_argument", "admin.token and triggers.token must contain distinct tokens")
	}
	credentials.triggerHash = sha256.Sum256([]byte(credentials.triggerToken))
	return credentials, nil
}

func (c instanceCredentials) acceptsTriggerToken(token string) bool {
	hash := sha256.Sum256([]byte(token))
	return c.triggerToken != "" && subtle.ConstantTimeCompare(hash[:], c.triggerHash[:]) == 1
}

func readCredential(path, name string, instance bool) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", os.ErrNotExist
	}
	invalid := problem("invalid_argument", name+" must be a readable private regular file")
	if instance {
		invalid = problem("invalid_argument", name+" must be a readable regular file with mode 0600")
	}
	if err != nil {
		return "", invalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || (instance && info.Mode().Perm() != 0o600) {
		return "", invalid
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", invalid
	}
	token := strings.TrimSpace(string(data))
	if !triggeridentity.ValidToken(token) {
		return "", problem("invalid_argument", name+" requires at least 32 bytes without whitespace or control characters")
	}
	return token, nil
}

func createCredential(path string) (string, error) {
	var entropy [32]byte
	rand.Read(entropy[:])
	token := base64.RawURLEncoding.EncodeToString(entropy[:])
	dir := filepath.Dir(path)
	failure := problem("unavailable", "cannot write Core credential "+filepath.Base(path))
	temp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return "", failure
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", failure
	}
	_, writeErr := temp.WriteString(token + "\n")
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return "", failure
	}
	// Publish the complete file atomically without replacing a provisioned token.
	if err := os.Link(temp.Name(), path); err != nil {
		return "", failure
	}
	if err := os.Remove(temp.Name()); err != nil {
		return "", failure
	}
	if err := syncCredentialDirectory(dir); err != nil {
		return "", err
	}
	return token, nil
}

func syncCredentialDirectory(path string) error {
	dir, err := os.Open(path)
	if err == nil {
		defer dir.Close()
		err = dir.Sync()
	}
	if err != nil {
		return problem("unavailable", "cannot sync Core credentials directory")
	}
	return nil
}
