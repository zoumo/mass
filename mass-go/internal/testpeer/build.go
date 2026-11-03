// Package testpeer connects MASS tests to the separately built agent-sdk-testify
// executable. It never builds or discovers an SDK-owned mock tool.
package testpeer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SDKVersion = "v0.0.0-20261102180816-a3a99ad44b90"

// Artifact is the installed external tool identity used by this test run.
type Artifact struct {
	Path    string
	Version string
	SHA256  string
}

// Launcher is one direct replacement for a single SDK protocol launch.
type Launcher struct {
	Path      string
	Provider  string
	Recording string
	Receipts  string
}

// Receipt is the persistent result written by one direct mock process.
type Receipt struct {
	SchemaVersion   int    `json:"schemaVersion"`
	RunID           string `json:"runId"`
	ConnectionID    string `json:"connectionId"`
	Status          string `json:"status"`
	Provider        string `json:"provider"`
	Recording       string `json:"recording"`
	PlanSHA256      string `json:"planSHA256"`
	RecordingSHA256 string `json:"recordingSHA256"`
	FullConsumption bool   `json:"fullConsumption"`
	ClientEOF       bool   `json:"clientEOF"`
	CleanupComplete bool   `json:"cleanupComplete"`
	NativeLaunched  bool   `json:"nativeLaunched"`
	ExitCode        int    `json:"exitCode"`
	Error           string `json:"error"`
}

// Executable returns the explicit test tool. A missing, relative or non-executable
// value is an error; tests never search PATH or fall back to a native Agent.
func Executable() (string, error) {
	path := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	if !filepath.IsAbs(path) {
		return "", errors.New("AGENT_SDK_TESTIFY_BIN must name an absolute separately built agent-sdk-testify executable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("AGENT_SDK_TESTIFY_BIN: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("AGENT_SDK_TESTIFY_BIN is not an executable file: %s", path)
	}
	return path, nil
}

// Inspect validates and identifies the explicitly installed external tool.
func Inspect(ctx context.Context) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	tool, err := Executable()
	if err != nil {
		return Artifact{}, err
	}
	version := exec.CommandContext(ctx, tool, "--version")
	out, err := version.CombinedOutput()
	if err != nil {
		return Artifact{}, errors.Join(fmt.Errorf("read external testify version: %w\n%s", err, out), ctx.Err())
	}
	binary, err := os.ReadFile(tool)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: tool, Version: strings.TrimSpace(string(out)), SHA256: fmt.Sprintf("%x", sha256.Sum256(binary))}, nil
}

// Direct binds one synthetic recording to an SDK executable path. Every launch
// gets a new persistent report outside the recording; --version probes do not
// consume the single protocol connection.
func Direct(dir, provider, recording string) (Launcher, error) {
	if provider != "codex" && provider != "claude" {
		return Launcher{}, fmt.Errorf("unsupported mock provider %q", provider)
	}
	tool, err := Executable()
	if err != nil {
		return Launcher{}, err
	}
	if !filepath.IsAbs(recording) {
		return Launcher{}, errors.New("direct mock recording must be absolute")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return Launcher{}, err
	}
	dir, err = os.MkdirTemp(dir, "direct-"+provider+"-")
	if err != nil {
		return Launcher{}, err
	}
	reports := filepath.Join(dir, "receipts")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		return Launcher{}, err
	}
	return (Launcher{Path: filepath.Join(dir, "peer"), Provider: provider, Recording: recording, Receipts: reports}).rebind(tool)
}

// Rebind keeps the executable identity while switching to another synthetic
// recording. Recovery tests use it after the previous native process exits.
func (l Launcher) Rebind(recording string) (Launcher, error) {
	if !filepath.IsAbs(recording) {
		return Launcher{}, errors.New("direct mock recording must be absolute")
	}
	if !filepath.IsAbs(l.Path) || !filepath.IsAbs(l.Receipts) {
		return Launcher{}, errors.New("direct mock launcher is not initialized")
	}
	tool, err := Executable()
	if err != nil {
		return Launcher{}, err
	}
	l.Recording = recording
	return l.rebind(tool)
}

func (l Launcher) rebind(tool string) (Launcher, error) {
	version := "0.0.0"
	var plan struct {
		CLIVersion string `json:"cliVersion"`
	}
	if err := readJSONFile(filepath.Join(l.Recording, "plan.json"), &plan); err == nil && plan.CLIVersion != "" {
		version = plan.CLIVersion
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\n" +
		"if [ \"$#\" -eq 1 ] && { [ \"$1\" = '--version' ] || [ \"$1\" = '-v' ]; }; then\n" +
		"  printf '%s\\n' " + quote(version) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"receipt_dir=$(/usr/bin/mktemp -d " + quote(filepath.Join(l.Receipts, "peer.XXXXXX")) + ") || exit\n" +
		"exec " + quote(tool) + " mock --provider " + l.Provider +
		" --recording " + quote(l.Recording) +
		" --report \"$receipt_dir/receipt.json\" -- \"$@\"\n"
	if err := os.WriteFile(l.Path, []byte(script), 0o700); err != nil {
		return Launcher{}, err
	}
	return l, nil
}

// ReceiptPaths returns the reports owned by launches through this launcher.
func (l Launcher) ReceiptPaths() ([]string, error) {
	return filepath.Glob(filepath.Join(l.Receipts, "peer.*", "receipt.json"))
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
