package sa

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const capabilityFileVersion = 1

type capabilityFile struct {
	Version      int    `json:"version"`
	Endpoint     string `json:"endpoint"`
	Token        string `json:"token"`
	ProjectID    string `json:"projectId"`
	Role         string `json:"role"`
	RootTicketID string `json:"rootTicketId,omitempty"`
	WorkerName   string `json:"workerName,omitempty"`
	AssignmentID string `json:"assignmentId,omitempty"`
}

// CapabilityGrant is returned only to the caller that creates the private
// capability file. Durable state retains the digest, never Token.
type CapabilityGrant struct {
	Token string
	State CapabilityState
}

func issueCapability(endpoint, path string, state CapabilityState) (CapabilityGrant, error) {
	if err := validateToolEndpoint(endpoint); err != nil {
		return CapabilityGrant{}, err
	}
	if !filepath.IsAbs(path) {
		return CapabilityGrant{}, operationError("invalid_argument", "invalid_capability_path", "capability file path must be absolute")
	}
	token, err := randomCapabilityToken()
	if err != nil {
		return CapabilityGrant{}, err
	}
	state.File = path
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}
	content := capabilityFile{
		Version:      capabilityFileVersion,
		Endpoint:     endpoint,
		Token:        token,
		ProjectID:    state.ProjectID,
		Role:         state.Role,
		RootTicketID: state.RootTicketID,
		WorkerName:   state.WorkerName,
		AssignmentID: state.AssignmentID,
	}
	if err := writeCapabilityFile(path, content); err != nil {
		return CapabilityGrant{}, err
	}
	return CapabilityGrant{Token: token, State: state}, nil
}

func randomCapabilityToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", operationError("unavailable", "capability_generation_failed", "capability token could not be generated")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func capabilityDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeCapabilityFile(path string, content capabilityFile) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability directory cannot be created")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability directory permissions cannot be set")
	}
	temp, err := os.CreateTemp(dir, ".capability-*")
	if err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability temporary file cannot be created")
	}
	tempName := temp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempName)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return operationError("unavailable", "capability_file_unavailable", "capability file permissions cannot be set")
	}
	encoder := json.NewEncoder(temp)
	if err = encoder.Encode(content); err != nil {
		_ = temp.Close()
		return operationError("unavailable", "capability_file_unavailable", "capability file cannot be encoded")
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return operationError("unavailable", "capability_file_unavailable", "capability file cannot be synchronized")
	}
	if err = temp.Close(); err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability file cannot be closed")
	}
	if err = os.Rename(tempName, path); err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability file cannot be replaced")
	}
	if err = syncDirectory(dir); err != nil {
		return operationError("unavailable", "capability_file_unavailable", "capability directory cannot be synchronized")
	}
	return nil
}

func readCapabilityFile(path string) (capabilityFile, error) {
	if !filepath.IsAbs(path) {
		return capabilityFile{}, operationError("invalid_argument", "invalid_capability_file", "capability file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return capabilityFile{}, operationError("failed_precondition", "invalid_capability_file", "capability file must be a regular 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return capabilityFile{}, operationError("unavailable", "capability_file_unavailable", "capability file cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var content capabilityFile
	if err := decoder.Decode(&content); err != nil {
		return capabilityFile{}, operationError("invalid_argument", "invalid_capability_file", "capability file is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return capabilityFile{}, operationError("invalid_argument", "invalid_capability_file", "capability file must contain one JSON object")
	}
	if content.Version != capabilityFileVersion || content.Token == "" ||
		content.ProjectID == "" || (content.Role != "router" && content.Role != "worker") {
		return capabilityFile{}, operationError("invalid_argument", "invalid_capability_file", "capability file is incomplete")
	}
	if err := validateToolEndpoint(content.Endpoint); err != nil {
		return capabilityFile{}, operationError("invalid_argument", "invalid_capability_file", "capability endpoint is invalid")
	}
	return content, nil
}

func removeCapabilityFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return operationError("unavailable", "capability_file_unavailable", "capability file cannot be removed")
	}
	return nil
}

func validateToolEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return operationError("invalid_argument", "invalid_tool_endpoint", "tool endpoint must be HTTP on loopback or HTTPS")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		ip := net.ParseIP(host)
		if ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return operationError("invalid_argument", "invalid_tool_endpoint", "tool endpoint must be HTTP on loopback or HTTPS")
}
