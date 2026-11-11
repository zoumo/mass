package recordingtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type DiagnosticOptions struct {
	Provider   string
	Executable string
	Model      string
	Output     string
	Smoke      bool
}

type DiagnosticStage struct {
	Name         string        `json:"name"`
	Command      []string      `json:"command,omitempty"`
	Budget       time.Duration `json:"budgetNanoseconds"`
	Elapsed      time.Duration `json:"elapsedNanoseconds"`
	PID          int           `json:"pid,omitempty"`
	ExitCode     int           `json:"exitCode"`
	TimedOut     bool          `json:"timedOut"`
	ProcessGone  bool          `json:"processGone"`
	GroupGone    bool          `json:"groupGone"`
	Stderr       string        `json:"redactedStderr,omitempty"`
	StderrSHA256 string        `json:"stderrSHA256,omitempty"`
	Error        string        `json:"error,omitempty"`
	ProcessRole  string        `json:"processRole,omitempty"`
	Signal       string        `json:"signal,omitempty"`
}

type DiagnosticReport struct {
	Provider       string            `json:"provider"`
	Status         string            `json:"status"`
	Blockers       []string          `json:"blockers"`
	Executable     string            `json:"executable"`
	Resolved       string            `json:"resolvedExecutable"`
	SHA256         string            `json:"executableSHA256"`
	Version        string            `json:"version"`
	TargetFormat   string            `json:"targetFormat"`
	Signature      string            `json:"codeSignature"`
	Model          string            `json:"model"`
	EffectiveModel string            `json:"effectiveModel,omitempty"`
	ModelSource    string            `json:"modelSource"`
	Authenticated  bool              `json:"cliAuthenticated"`
	AuthMethod     string            `json:"authMethod,omitempty"`
	APIProvider    string            `json:"apiProvider,omitempty"`
	Platform       string            `json:"platform"`
	SDK            string            `json:"sdk"`
	Testify        string            `json:"testify"`
	Stages         []DiagnosticStage `json:"stages"`
	Boundaries     map[string]string `json:"boundaries"`
	LastStage      string            `json:"lastStage"`
	RecordingRoot  string            `json:"recordingRoot,omitempty"`
	SDKCleanup     string            `json:"sdkCleanup"`
	ListenerClosed *bool             `json:"listenerClosed,omitempty"`
	Cleanup        map[string]string `json:"cleanup"`
	FlowBudget     time.Duration     `json:"flowBudgetNanoseconds,omitempty"`
	FlowElapsed    time.Duration     `json:"flowElapsedNanoseconds,omitempty"`
	FlowTimedOut   bool              `json:"flowTimedOut"`
	Artifacts      []DiagnosticFile  `json:"retainedArtifacts,omitempty"`
}

type DiagnosticFile struct {
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	RedactedPath   string `json:"redactedPath,omitempty"`
	RedactedSHA256 string `json:"redactedSHA256,omitempty"`
}

type diagnostic struct {
	root   string
	report DiagnosticReport
}

func newDiagnostic(output, provider string) (*diagnostic, error) {
	if !filepath.IsAbs(output) {
		return nil, errors.New("diagnostic output must be an absolute, nonexistent directory")
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return nil, fmt.Errorf("reserve diagnostic directory: %w", err)
	}
	return &diagnostic{root: output, report: DiagnosticReport{
		Provider: provider, Status: "blocker", SDK: RuntimeSDKVersion, Testify: TestifyVersion,
		Platform: goruntime.GOOS + "/" + goruntime.GOARCH, SDKCleanup: "not_started",
		Blockers: []string{}, Stages: []DiagnosticStage{}, Cleanup: map[string]string{},
		Boundaries: map[string]string{
			"native_open": "not_executed", "native_input_ack": "not_executed",
			"native_progress": "not_executed", "native_completion": "not_executed",
			"sdk": "not_executed", "mass_relay_sse": "not_executed",
			"history_commit": "not_executed", "testify_export_strict_replay": "not_executed",
		},
	}}, nil
}

func (d *diagnostic) commandPID(role string, pid int) {
	d.report.Stages = append(d.report.Stages, DiagnosticStage{Name: role, PID: pid, ProcessRole: role, ExitCode: -1})
	_ = d.save()
}

func (d *diagnostic) setCleanup(role, result string) {
	d.report.Cleanup[role] = result
	_ = d.save()
}

func (d *diagnostic) inspectExecutable(ctx context.Context, executable string) error {
	d.phase("executable")
	path, err := exec.LookPath(executable)
	if err != nil {
		return fmt.Errorf("executable unavailable: %w", err)
	}
	d.report.Executable, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	d.report.Resolved, err = filepath.EvalSymlinks(d.report.Executable)
	if err != nil {
		return err
	}
	d.report.SHA256, err = fileSHA256(d.report.Resolved)
	if err != nil {
		return err
	}
	if fileTool, lookErr := exec.LookPath("file"); lookErr == nil {
		out, checkErr := d.command(ctx, 10*time.Second, "architecture", fileTool, d.report.Resolved)
		if checkErr != nil {
			return fmt.Errorf("architecture inspection failed: %w", checkErr)
		}
		d.report.TargetFormat = strings.TrimSpace(string(out))
	}
	d.report.Signature = "not_applicable"
	if goruntime.GOOS == "darwin" && strings.Contains(d.report.TargetFormat, "Mach-O") {
		if codesign, lookErr := exec.LookPath("codesign"); lookErr == nil {
			if _, checkErr := d.command(ctx, 10*time.Second, "signature", codesign, "--verify", "--strict", "--verbose=2", d.report.Resolved); checkErr == nil {
				d.report.Signature = "valid"
			} else {
				d.report.Signature = "invalid"
			}
		}
	}
	return nil
}

func (d *diagnostic) retainArtifacts(root string) {
	names := map[string]bool{
		"report.json": true, "manifest.json": true, "raw.jsonl": true, "stderr.raw": true,
		"execution.json": true, "plan.json": true, "daemon.log": true,
	}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !names[entry.Name()] {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		digest, hashErr := fileSHA256(path)
		if hashErr != nil {
			return hashErr
		}
		if hashErr == nil {
			artifact := DiagnosticFile{Path: path, Size: info.Size(), SHA256: digest}
			if entry.Name() == "stderr.raw" || entry.Name() == "daemon.log" {
				if data, readErr := os.ReadFile(path); readErr == nil {
					redacted := filepath.Join(d.root, fmt.Sprintf("retained-%d.stderr.txt", len(d.report.Artifacts)+1))
					if writeErr := os.WriteFile(redacted, redactDiagnostic(data), 0o600); writeErr == nil {
						artifact.RedactedPath = redacted
						artifact.RedactedSHA256, _ = fileSHA256(redacted)
					}
				}
			}
			d.report.Artifacts = append(d.report.Artifacts, artifact)
		}
		return nil
	})
	if walkErr != nil {
		d.report.Cleanup["artifact_retention"] = string(redactDiagnostic([]byte(walkErr.Error())))
	}
}

func (d *diagnostic) observeNative(root string) {
	paths, _ := filepath.Glob(filepath.Join(root, "session", "executions", "*", "record", "raw", "raw.jsonl"))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		decoder := json.NewDecoder(file)
		for {
			var record struct {
				Direction string `json:"direction"`
				Wire      struct {
					Method  string `json:"method"`
					Type    string `json:"type"`
					Subtype string `json:"subtype"`
					IsError bool   `json:"is_error"`
					Params  struct {
						Turn struct {
							Status string `json:"status"`
						} `json:"turn"`
					} `json:"params"`
				} `json:"wire"`
			}
			if err := decoder.Decode(&record); err != nil {
				if !errors.Is(err, io.EOF) {
					d.report.Cleanup["native_evidence"] = "partial_or_unreadable"
				}
				break
			}
			if record.Direction != "s2c" {
				continue
			}
			switch record.Wire.Method {
			case "thread/started":
				d.report.Boundaries["native_open"] = "observed_raw_thread_started"
			case "turn/started":
				d.report.Boundaries["native_input_ack"] = "observed_raw_turn_started"
			case "item/agentMessage/delta":
				d.report.Boundaries["native_progress"] = "observed_raw_agent_message_delta"
			case "turn/completed":
				if record.Wire.Params.Turn.Status == "completed" {
					d.report.Boundaries["native_completion"] = "observed_raw_turn_completed"
				}
			}
			if record.Wire.Type == "assistant" {
				d.report.Boundaries["native_progress"] = "observed_raw_assistant"
			}
			if record.Wire.Type == "result" && record.Wire.Subtype == "success" && !record.Wire.IsError {
				d.report.Boundaries["native_completion"] = "observed_raw_result_success"
			}
		}
		_ = file.Close()
	}
}

func (d *diagnostic) save() error {
	data, err := json.MarshalIndent(d.report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.root, "diagnosis.json"), append(data, '\n'), 0o600)
}

func (d *diagnostic) phase(name string) {
	d.report.LastStage = name
	_ = d.save()
}

func redactDiagnostic(data []byte) []byte {
	text := string(data)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if len(value) >= 4 && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "AUTH")) {
			text = strings.ReplaceAll(text, value, "<REDACTED>")
		}
	}
	text = regexp.MustCompile(`(?i)(bearer\s+|(?:api[_-]?key|token|password|secret)\s*[:=]\s*)[^\s",]+`).ReplaceAllString(text, "${1}<REDACTED>")
	return []byte(text)
}

func processGone(pid int) bool {
	return pid > 0 && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func groupGone(pid int) bool {
	return pid > 0 && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH)
}

func waitGroupGone(pid int) bool {
	deadline := time.Now().Add(500 * time.Millisecond)
	for !groupGone(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func (d *diagnostic) command(ctx context.Context, budget time.Duration, name, executable string, args ...string) ([]byte, error) {
	d.phase(name)
	stageCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	command := exec.CommandContext(stageCtx, executable, args...)
	command.Dir = d.root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	started := time.Now()
	stage := DiagnosticStage{Name: name, Budget: budget, ExitCode: -1}
	for _, argument := range append([]string{executable}, args...) {
		stage.Command = append(stage.Command, string(redactDiagnostic([]byte(argument))))
	}
	err := command.Start()
	if err == nil {
		stage.PID = command.Process.Pid
		err = command.Wait()
		stage.ExitCode = command.ProcessState.ExitCode()
		if !groupGone(stage.PID) {
			_ = syscall.Kill(-stage.PID, syscall.SIGKILL)
		}
		stage.ProcessGone = processGone(stage.PID)
		stage.GroupGone = waitGroupGone(stage.PID)
	}
	stage.Elapsed = time.Since(started)
	stage.TimedOut = errors.Is(stageCtx.Err(), context.DeadlineExceeded)
	if err != nil {
		stage.Error = string(redactDiagnostic([]byte(err.Error())))
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				stage.Signal = status.Signal().String()
			}
		}
	}
	prefix := fmt.Sprintf("%03d-%s", len(d.report.Stages)+1, name)
	stage.Stderr = filepath.Join(d.root, prefix+".stderr.txt")
	stderrData := stderr.Bytes()
	if name == "auth" {
		stderrData = []byte("CLI authentication status checked; output intentionally not retained\n")
	}
	if writeErr := os.WriteFile(stage.Stderr, redactDiagnostic(stderrData), 0o600); writeErr != nil {
		err = errors.Join(err, writeErr)
	}
	stage.StderrSHA256, _ = fileSHA256(stage.Stderr)
	d.report.Stages = append(d.report.Stages, stage)
	if name != "auth" {
		if writeErr := os.WriteFile(filepath.Join(d.root, prefix+".stdout.txt"), redactDiagnostic(stdout.Bytes()), 0o600); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}
	if stage.PID > 0 && (!stage.ProcessGone || !stage.GroupGone) {
		err = errors.Join(err, errors.New("owned process cleanup not confirmed"))
	}
	_ = d.save()
	return stdout.Bytes(), err
}

func explicitRecordingModel(model string) (string, string) {
	if selected := strings.TrimSpace(model); selected != "" {
		return selected, "argument"
	}
	if selected := strings.TrimSpace(os.Getenv("MASS_RECORD_MODEL")); selected != "" {
		return selected, "MASS_RECORD_MODEL"
	}
	return "", "missing"
}

func (d *diagnostic) preflight(ctx context.Context, options DiagnosticOptions) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	d.report.Model, d.report.ModelSource = explicitRecordingModel(options.Model)
	if options.Provider != "codex" && options.Provider != "claude" {
		return errors.New("provider must be codex or claude")
	}
	executable := options.Executable
	if executable == "" {
		executable = os.Getenv("MASS_RECORD_" + strings.ToUpper(options.Provider))
	}
	if executable == "" {
		executable = options.Provider
	}
	if err := d.inspectExecutable(ctx, executable); err != nil {
		return err
	}
	for _, check := range []struct {
		name string
		args []string
	}{
		{"version", []string{"--version"}},
		{"help", []string{"--help"}},
	} {
		out, checkErr := d.command(ctx, 10*time.Second, check.name, d.report.Executable, check.args...)
		if check.name == "version" {
			d.report.Version = strings.TrimSpace(string(redactDiagnostic(out)))
		}
		if checkErr != nil {
			return fmt.Errorf("%s failed: %w", check.name, checkErr)
		}
	}
	if options.Provider == "claude" {
		parts := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(d.report.Version)
		if len(parts) != 4 {
			return errors.New("claude version could not be parsed")
		}
		major, _ := strconv.Atoi(parts[1])
		minor, _ := strconv.Atoi(parts[2])
		patch, _ := strconv.Atoi(parts[3])
		if major < 2 || major == 2 && (minor < 1 || minor == 1 && patch < 258) {
			return errors.New("claude CLI 2.1.258 or later required for current SDK resume gate")
		}
	}
	helpArgs, authArgs := []string{"app-server", "--help"}, []string{"login", "status"}
	if options.Provider == "claude" {
		helpArgs, authArgs = []string{"auth", "status", "--help"}, []string{"auth", "status"}
	}
	if _, err := d.command(ctx, 10*time.Second, "command-help", d.report.Executable, helpArgs...); err != nil {
		return fmt.Errorf("command help failed: %w", err)
	}
	auth, authErr := d.command(ctx, 10*time.Second, "auth", d.report.Executable, authArgs...)
	if options.Provider == "codex" {
		// Codex writes login status to stderr; retain only the exit status.
		d.report.Authenticated = authErr == nil
		d.report.AuthMethod = "cli_login"
	} else {
		var status struct {
			LoggedIn    bool   `json:"loggedIn"`
			AuthMethod  string `json:"authMethod"`
			APIProvider string `json:"apiProvider"`
		}
		d.report.Authenticated = authErr == nil && json.Unmarshal(auth, &status) == nil && status.LoggedIn
		d.report.AuthMethod = status.AuthMethod
		d.report.APIProvider = status.APIProvider
	}
	if !d.report.Authenticated {
		d.report.Blockers = append(d.report.Blockers, "CLI authentication status not confirmed")
	}
	if d.report.Model == "" {
		d.report.Blockers = append(d.report.Blockers, "explicit model required: --model or MASS_RECORD_MODEL; personal settings are not consulted")
	}
	if len(d.report.Blockers) > 0 {
		return errors.New("preflight prerequisites missing")
	}
	return nil
}

func Diagnose(ctx context.Context, options DiagnosticOptions) (report DiagnosticReport, resultErr error) {
	d, err := newDiagnostic(options.Output, options.Provider)
	if err != nil {
		return report, err
	}
	defer func() {
		if resultErr != nil {
			d.report.Status = "blocker"
			d.report.Blockers = append(d.report.Blockers, string(redactDiagnostic([]byte(resultErr.Error()))))
		}
		resultErr = errors.Join(resultErr, d.save())
		report = d.report
	}()
	if err := d.preflight(ctx, options); err != nil {
		return report, err
	}
	d.report.Status = "preflight_only"
	if !options.Smoke {
		return report, nil
	}
	args := []string{
		"exec", "--json", "--ignore-user-config", "--ignore-rules", "--ephemeral",
		"--skip-git-repo-check", "--sandbox", "read-only", "--model", d.report.Model,
		"-c", "project_doc_max_bytes=0", "-c", "project_root_markers=[]", prompt,
	}
	if options.Provider == "claude" {
		args = []string{
			"-p", prompt, "--model", d.report.Model, "--output-format", "stream-json", "--verbose",
			"--tools", "", "--safe-mode", "--setting-sources", "user", "--strict-mcp-config",
		}
	}
	out, err := d.command(ctx, time.Minute, "native-smoke", d.report.Executable, args...)
	if err != nil {
		return report, fmt.Errorf("native smoke failed: %w", err)
	}
	completed, message := false, false
	for line := range strings.SplitSeq(string(out), "\n") {
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Model   string `json:"model"`
			Item    struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if options.Provider == "codex" {
			completed = completed || event.Type == "turn.completed"
			message = message || event.Type == "item.completed" && event.Item.Type == "agent_message" && event.Item.Text != ""
		} else {
			if event.Type == "system" && event.Subtype == "init" && event.Model != "" {
				d.report.EffectiveModel = event.Model
			}
			completed = completed || event.Type == "result" && event.Subtype == "success" && !event.IsError
			message = message || event.Type == "result" && event.Result != ""
		}
	}
	if !completed || !message {
		return report, errors.New("native smoke exited without successful completion and assistant message")
	}
	d.report.Status = "ready"
	d.report.Boundaries["native_completion"] = "observed_native_cli_smoke"
	return report, nil
}
