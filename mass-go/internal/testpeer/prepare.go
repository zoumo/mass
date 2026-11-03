package testpeer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const externalCommandTimeout = 10 * time.Second

// Run is the public prepared-mock descriptor subset used by MASS tests.
type Run struct {
	SchemaVersion   int               `json:"schemaVersion"`
	RunID           string            `json:"runId"`
	Tool            Tool              `json:"tool"`
	ToolVersion     string            `json:"toolVersion"`
	ToolSHA256      string            `json:"toolSHA256"`
	Recording       string            `json:"recording"`
	Provider        string            `json:"provider"`
	CaseID          string            `json:"caseId"`
	ResourceRoot    string            `json:"resourceRoot"`
	Workspace       string            `json:"workspace"`
	ProtocolCWD     string            `json:"protocolCwd"`
	Env             map[string]string `json:"env"`
	Contract        string            `json:"contract"`
	ContractSHA     string            `json:"contractSHA256"`
	Materialized    string            `json:"materialization"`
	MaterializedSHA string            `json:"materializationSHA256"`
	Connections     []Connection      `json:"connections"`
	FinishCommand   Command           `json:"finish"`
	Descriptor      string            `json:"-"`
	Output          string            `json:"-"`
}

// Tool is the installed executable selected by the prepared run.
type Tool struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// Command is an absolute executable plus arguments from the descriptor.
type Command struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Connection is one declared native protocol stream.
type Connection struct {
	ID              string   `json:"id"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	Launcher        string   `json:"launcher"`
	Recording       string   `json:"recording"`
	PlanSHA256      string   `json:"planSHA256"`
	ManifestSHA256  string   `json:"manifestSHA256"`
	RecordingSHA256 string   `json:"recordingSHA256"`
	Receipt         string   `json:"receipt"`
	ProcessReceipt  string   `json:"processReceipt"`
}

// Prepare authors external synthetic material and creates one prepared run.
// The caller owns output and applies Env to the SDK host process.
func Prepare(ctx context.Context, dir, provider string, scenario []byte) (*Run, error) {
	tool, err := Executable()
	if err != nil {
		return nil, err
	}
	material, err := Author(dir, provider, scenario)
	if err != nil {
		return nil, err
	}
	output := filepath.Join(dir, "prepared-"+filepath.Base(material.Root))
	ctx, cancel := context.WithTimeout(ctx, externalCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "mock", "--prepare", "--recording", material.Recording, "--output", output)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare external mock: %w\n%s", err, stderr.String()), ctx.Err())
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("decode prepared descriptor: %w", err)
	}
	run.Descriptor, err = filepath.EvalSymlinks(filepath.Join(output, "run.json"))
	if err != nil {
		return nil, fmt.Errorf("resolve prepared descriptor: %w", err)
	}
	run.Output = filepath.Dir(run.Descriptor)
	if err := run.validate(ctx, provider); err != nil {
		return nil, err
	}
	var persisted Run
	if err := readJSONFile(run.Descriptor, &persisted); err != nil {
		return nil, fmt.Errorf("read prepared descriptor: %w", err)
	}
	persisted.Descriptor, persisted.Output = run.Descriptor, run.Output
	if !reflect.DeepEqual(run, persisted) {
		return nil, errors.New("prepared command output differs from run.json")
	}
	return &run, nil
}

// RuntimeRoot returns the tool-owned temporary root.
func (r *Run) RuntimeRoot() string { return r.ResourceRoot }

func (r *Run) validate(ctx context.Context, provider string) error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("prepared mock schemaVersion %d is unsupported", r.SchemaVersion)
	}
	if r.RunID == "" || r.ToolVersion == "" || r.ToolSHA256 == "" || r.Provider != provider || r.CaseID == "" ||
		!filepath.IsAbs(r.Tool.Command) || !filepath.IsAbs(r.ResourceRoot) ||
		!filepath.IsAbs(r.Workspace) || !filepath.IsAbs(r.ProtocolCWD) ||
		!filepath.IsAbs(r.Recording) || !filepath.IsAbs(r.Contract) ||
		!filepath.IsAbs(r.Materialized) || r.ContractSHA == "" || r.MaterializedSHA == "" ||
		len(r.Env) == 0 || len(r.Connections) == 0 || !filepath.IsAbs(r.Descriptor) ||
		!filepath.IsAbs(r.FinishCommand.Command) {
		return errors.New("prepared mock descriptor is incomplete")
	}
	if !within(r.Contract, r.Output) || !within(r.Materialized, r.Output) {
		return errors.New("prepared mock contract or materialization is outside its output")
	}
	for name, expected := range map[string]string{r.Contract: r.ContractSHA, r.Materialized: r.MaterializedSHA} {
		actual, err := fileSHA256(name)
		if err != nil {
			return fmt.Errorf("read prepared material %s: %w", name, err)
		}
		if actual != expected {
			return fmt.Errorf("prepared material hash mismatch: %s", name)
		}
	}
	tool, err := Executable()
	if err != nil {
		return err
	}
	toolInfo, err := os.Stat(tool)
	if err != nil {
		return err
	}
	descriptorTool, err := os.Stat(r.Tool.Command)
	if err != nil || !os.SameFile(toolInfo, descriptorTool) {
		return errors.New("prepared mock tool differs from AGENT_SDK_TESTIFY_BIN")
	}
	data, err := os.ReadFile(tool)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != r.ToolSHA256 {
		return errors.New("prepared mock toolSHA256 mismatch")
	}
	artifact, err := Inspect(ctx)
	if err != nil {
		return err
	}
	if !toolVersionMatches(artifact.Version, r.ToolVersion) {
		return errors.New("prepared mock toolVersion mismatch")
	}
	finishInfo, err := os.Stat(r.FinishCommand.Command)
	if err != nil || !os.SameFile(toolInfo, finishInfo) {
		return errors.New("prepared mock finish command differs from the installed tool")
	}
	output := filepath.Dir(r.Descriptor) + string(filepath.Separator)
	ids := make(map[string]bool, len(r.Connections))
	receipts := make(map[string]bool, len(r.Connections)*2)
	for _, connection := range r.Connections {
		if connection.ID == "" || ids[connection.ID] || !filepath.IsAbs(connection.Command) ||
			!filepath.IsAbs(connection.Launcher) || !filepath.IsAbs(connection.Recording) ||
			connection.PlanSHA256 == "" || connection.ManifestSHA256 == "" ||
			connection.RecordingSHA256 == "" || !filepath.IsAbs(connection.Receipt) ||
			!filepath.IsAbs(connection.ProcessReceipt) ||
			!strings.HasPrefix(connection.Receipt, output) ||
			!strings.HasPrefix(connection.ProcessReceipt, output) {
			return fmt.Errorf("prepared mock connection descriptor is incomplete: id=%q command=%q launcher=%q recording=%q plan=%t manifest=%t recordingHash=%t receipt=%q processReceipt=%q output=%q",
				connection.ID, connection.Command, connection.Launcher, connection.Recording,
				connection.PlanSHA256 != "", connection.ManifestSHA256 != "", connection.RecordingSHA256 != "",
				connection.Receipt, connection.ProcessReceipt, output)
		}
		ids[connection.ID] = true
		commandInfo, err := os.Stat(connection.Command)
		if err != nil || !os.SameFile(toolInfo, commandInfo) {
			return fmt.Errorf("prepared mock connection command differs from the installed tool: id=%q", connection.ID)
		}
		launcherInfo, err := os.Stat(connection.Launcher)
		if err != nil || !launcherInfo.Mode().IsRegular() || launcherInfo.Mode().Perm()&0o111 == 0 ||
			!within(connection.Launcher, r.ResourceRoot) {
			return fmt.Errorf("prepared mock connection launcher is invalid: id=%q", connection.ID)
		}
		if connection.PlanSHA256 != r.ContractSHA {
			return fmt.Errorf("prepared mock connection plan hash differs: id=%q", connection.ID)
		}
		for _, receipt := range []string{connection.Receipt, connection.ProcessReceipt} {
			if receipts[receipt] {
				return fmt.Errorf("prepared mock connection reuses a receipt path: id=%q", connection.ID)
			}
			receipts[receipt] = true
		}
	}
	return nil
}

func toolVersionMatches(installed, prepared string) bool {
	return prepared != "" && strings.HasSuffix(installed, prepared)
}

func within(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// Connection returns one declared stream or an error.
func (r *Run) Connection(id string) (Connection, error) {
	for _, connection := range r.Connections {
		if connection.ID == id {
			return connection, nil
		}
	}
	return Connection{}, fmt.Errorf("prepared mock connection %q is not declared", id)
}

// Finish closes all connections and verifies their receipts and cleanup.
func (r *Run) Finish(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, externalCommandTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finish external mock: %w", err)
	}
	var persisted Run
	if err := readJSONFile(r.Descriptor, &persisted); err != nil {
		return fmt.Errorf("read prepared descriptor before finish: %w", err)
	}
	persisted.Descriptor, persisted.Output = r.Descriptor, r.Output
	if err := persisted.validate(ctx, r.Provider); err != nil {
		return err
	}
	if !reflect.DeepEqual(persisted, *r) {
		return errors.New("prepared descriptor changed before finish")
	}
	cmd := exec.CommandContext(ctx, persisted.FinishCommand.Command, persisted.FinishCommand.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	commandErr := cmd.Run()
	var result struct {
		SchemaVersion   int    `json:"schemaVersion"`
		RunID           string `json:"runId"`
		Status          string `json:"status"`
		CleanupComplete bool   `json:"cleanupComplete"`
		Error           string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return errors.Join(fmt.Errorf("finish external mock: %w", commandErr),
			fmt.Errorf("decode finish result: %w", err), errors.New(stderr.String()), ctx.Err())
	}
	if result.SchemaVersion != 1 || result.RunID != r.RunID ||
		(result.Status != "passed" && result.Status != "failed") {
		return errors.New("mock finish identity mismatch")
	}
	var finish struct {
		SchemaVersion   int    `json:"schemaVersion"`
		RunID           string `json:"runId"`
		Status          string `json:"status"`
		CleanupComplete bool   `json:"cleanupComplete"`
		Error           string `json:"error"`
		Connections     []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"connections"`
		History struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"history"`
	}
	if err := readJSON(filepath.Join(r.Output, "finish.json"), &finish); err != nil {
		return errors.Join(fmt.Errorf("read mock finish: %w", err), commandErr, ctx.Err())
	}
	if finish.SchemaVersion != 1 || finish.RunID != r.RunID || finish.Status != result.Status ||
		finish.Error != result.Error || finish.CleanupComplete != result.CleanupComplete {
		return errors.New("mock finish.json differs from command result")
	}
	if finish.History.Status != "passed" {
		return fmt.Errorf("mock finish does not prove history completion: %s", finish.History.Error)
	}
	if len(finish.Connections) != len(r.Connections) {
		return errors.New("mock finish does not report every declared connection")
	}
	for index, connection := range r.Connections {
		reported := finish.Connections[index]
		if reported.ID != connection.ID || reported.Status != "passed" {
			return fmt.Errorf("mock finish connection failed: %s: %s", reported.ID, reported.Error)
		}
	}
	var cleanup struct {
		SchemaVersion   int    `json:"schemaVersion"`
		RunID           string `json:"runId"`
		CleanupComplete bool   `json:"cleanupComplete"`
		History         struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"history"`
	}
	if err := readJSON(filepath.Join(r.Output, "cleanup.json"), &cleanup); err != nil {
		return errors.Join(fmt.Errorf("read mock cleanup: %w", err), commandErr, ctx.Err())
	}
	if cleanup.SchemaVersion != 1 || cleanup.RunID != r.RunID || !cleanup.CleanupComplete {
		return errors.New("mock cleanup.json does not prove complete cleanup")
	}
	if cleanup.History.Status != "passed" {
		return fmt.Errorf("mock cleanup does not prove history completion: %s", cleanup.History.Error)
	}
	for _, connection := range r.Connections {
		var receipt Receipt
		if err := readJSON(connection.Receipt, &receipt); err != nil {
			return errors.Join(fmt.Errorf("read %s receipt: %w", connection.ID, err), commandErr, ctx.Err())
		}
		if receipt.SchemaVersion != 1 || receipt.RunID != r.RunID || receipt.ConnectionID != connection.ID ||
			receipt.Provider != r.Provider || receipt.Recording != connection.Recording ||
			receipt.PlanSHA256 != connection.PlanSHA256 ||
			receipt.RecordingSHA256 != connection.RecordingSHA256 ||
			receipt.Status != "passed" || !receipt.FullConsumption || !receipt.ClientEOF ||
			!receipt.CleanupComplete || receipt.NativeLaunched || receipt.ExitCode != 0 || receipt.Error != "" {
			return errors.New("mock connection receipt does not prove strict protocol completion")
		}
		var process struct {
			SchemaVersion int    `json:"schemaVersion"`
			RunID         string `json:"runId"`
			ConnectionID  string `json:"connectionId"`
			Status        string `json:"status"`
			ExitCode      int    `json:"exitCode"`
			Tool          string `json:"tool"`
			ToolVersion   string `json:"toolVersion"`
			ToolSHA256    string `json:"toolSHA256"`
			Error         string `json:"error"`
		}
		if err := readJSON(connection.ProcessReceipt, &process); err != nil {
			return errors.Join(fmt.Errorf("read %s process receipt: %w", connection.ID, err), commandErr, ctx.Err())
		}
		toolInfo, toolErr := os.Stat(connection.Command)
		processInfo, processErr := os.Stat(process.Tool)
		if toolErr != nil || processErr != nil || !os.SameFile(toolInfo, processInfo) {
			return fmt.Errorf("mock %s process receipt names a different tool", connection.ID)
		}
		if process.SchemaVersion != 1 || process.RunID != r.RunID ||
			process.ConnectionID != connection.ID || process.Status != "passed" ||
			process.ExitCode != 0 || process.ToolVersion != r.ToolVersion ||
			process.ToolSHA256 != r.ToolSHA256 || process.Error != "" {
			return errors.New("mock process receipt does not prove normal exit")
		}
	}
	if _, err := os.Stat(r.ResourceRoot); !os.IsNotExist(err) {
		return errors.New("prepared mock resource root still exists after finish")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finish external mock: %w", err)
	}
	if commandErr != nil {
		if result.Error != "" {
			return fmt.Errorf("finish external mock: %w: %s", commandErr, result.Error)
		}
		return fmt.Errorf("finish external mock: %w", commandErr)
	}
	if result.Status != "passed" || !result.CleanupComplete {
		return fmt.Errorf("mock finish failed: %s", result.Error)
	}
	return nil
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
