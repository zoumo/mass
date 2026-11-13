package recordingtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agenttestify "github.com/zoumo/agent-sdk-testify"
)

type artifactCase struct {
	provider   string
	name       string
	suite      string
	sdk        string
	testify    string
	materialID string
	flow       flowCase
}

var recordingArtifacts = []artifactCase{
	{
		provider: "codex",
		name:     "basic",
		flow:     flowCase{name: "basic", provider: "codex", prompt: prompt, project: project},
	},
	{
		provider: "codex",
		name:     "permission",
		flow:     flowCase{name: "permission", provider: "codex", prompt: permissionPrompt, project: permissionProject, expectPermission: true},
	},
	{
		provider: "claude",
		name:     "basic",
		flow:     flowCase{name: "basic", provider: "claude", prompt: claudePrompt, project: claudeProject, initialInput: true, ignoreStreamKinds: true},
	},
	{
		provider: "claude",
		name:     "permission",
		flow:     flowCase{name: "permission", provider: "claude", prompt: claudePermissionPrompt, project: claudePermissionProject, initialInput: true, ignoreStreamKinds: true, expectPermission: true},
	},
}

type materialArtifact struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Action          string            `json:"action"`
	Provenance      string            `json:"provenance"`
	Path            string            `json:"path"`
	PlanSHA256      string            `json:"planSHA256"`
	ManifestSHA256  string            `json:"manifestSHA256"`
	RecordingSHA256 string            `json:"recordingSHA256"`
	Connections     []string          `json:"connections"`
	CreatedAt       string            `json:"createdAt"`
	Files           map[string]string `json:"files"`
}

type recordingFormat struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Transport string `json:"transport"`
}

type nativeIdentity struct {
	Executable string   `json:"executable"`
	SHA256     string   `json:"sha256"`
	Args       []string `json:"args"`
	CWD        string   `json:"cwd"`
	Version    string   `json:"version"`
}

type captureManifest struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Status          string            `json:"status"`
	CaptureStatus   string            `json:"captureStatus"`
	CredentialScan  string            `json:"credentialScan"`
	Provenance      string            `json:"provenance"`
	PlanSHA256      string            `json:"planSHA256"`
	RunSHA256       string            `json:"runSHA256"`
	ProxyVersion    string            `json:"proxyVersion"`
	RecordingFormat recordingFormat   `json:"recordingFormat"`
	Native          nativeIdentity    `json:"native"`
	Files           map[string]string `json:"files"`
	SavedFiles      map[string]string `json:"savedFiles"`
}

type rawManifest struct {
	Version         int               `json:"version"`
	CaptureStatus   string            `json:"captureStatus"`
	ProxyVersion    string            `json:"proxyVersion"`
	RecordingFormat recordingFormat   `json:"recordingFormat"`
	Native          nativeIdentity    `json:"native"`
	Files           map[string]string `json:"files"`
}

type captureReport struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Status          string `json:"status"`
	CaptureStatus   string `json:"captureStatus"`
	Consumer        bool   `json:"consumer"`
	Provenance      string `json:"provenance"`
	NativeLaunched  bool   `json:"nativeLaunched"`
	ClientEOF       bool   `json:"clientEOF"`
	CleanupComplete bool   `json:"cleanupComplete"`
}

type capturePlan struct {
	Version       int    `json:"version"`
	AgentType     string `json:"agentType"`
	CaseID        string `json:"caseId"`
	DriverProfile string `json:"driverProfile"`
	Project       string `json:"project"`
	CLIVersion    string `json:"cliVersion"`
	Model         string `json:"model"`
}

func RunGate(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, validateRecordingCaseSelection(root))
	require.NoError(t, validateActiveRecordingSelection(activeRecordingArtifacts))
	for _, spec := range allRecordingArtifacts() {
		material, index, err := auditRecordingCase(root, spec)
		require.NoError(t, err)
		for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl"} {
			info, err := os.Stat(filepath.Join(material, filepath.FromSlash(name)))
			require.NoError(t, err)
			t.Logf("%s/%s %-16s bytes=%d sha256=%s", spec.provider, spec.name, name, info.Size(), index.Hashes[name])
		}
	}
}

func validateRecordingCaseSelection(root string) error {
	simulation := filepath.Join(root, "tests", "testdata", "simulation")
	suites := map[string]map[string][]string{}
	for _, spec := range allRecordingArtifacts() {
		if suites[spec.directory()] == nil {
			suites[spec.directory()] = map[string][]string{}
		}
		suites[spec.directory()][spec.provider] = append(suites[spec.directory()][spec.provider], spec.name)
	}
	entries, err := os.ReadDir(simulation)
	if err != nil {
		return err
	}
	if err := compareDirectoryNames(entries, slices.Collect(maps.Keys(suites))); err != nil {
		return fmt.Errorf("recording suite selection: %w", err)
	}
	for suite, providers := range suites {
		recordings := filepath.Join(simulation, suite)
		entries, err := os.ReadDir(recordings)
		if err != nil {
			return err
		}
		if err := compareDirectoryNames(entries, slices.Collect(maps.Keys(providers))); err != nil {
			return fmt.Errorf("recording provider selection for %s: %w", suite, err)
		}
		for provider, cases := range providers {
			entries, err := os.ReadDir(filepath.Join(recordings, provider))
			if err != nil {
				return err
			}
			if err := compareDirectoryNames(entries, cases); err != nil {
				return fmt.Errorf("recording case selection for %s/%s: %w", suite, provider, err)
			}
		}
	}
	return nil
}

func compareDirectoryNames(entries []os.DirEntry, expected []string) error {
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("unexpected non-directory %q", entry.Name())
		}
		actual = append(actual, entry.Name())
	}
	return compareNames(actual, expected)
}

func compareEntryNames(entries []os.DirEntry, expected []string) error {
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	return compareNames(actual, expected)
}

func compareNames(actual, expected []string) error {
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}
	if len(actual) != len(want) {
		return fmt.Errorf("got %v, want %v", actual, expected)
	}
	for _, name := range actual {
		if !want[name] {
			return fmt.Errorf("unexpected entry %q", name)
		}
	}
	return nil
}

func auditRecordingCase(root string, spec artifactCase) (string, caseIndex, error) {
	if spec.provider != "codex" && spec.provider != "claude" {
		return "", caseIndex{}, fmt.Errorf("unsupported provider %q", spec.provider)
	}
	caseRoot := filepath.Join(root, spec.casePath())
	material := filepath.Join(caseRoot, "material")
	indexPath := filepath.Join(caseRoot, "index.json")
	if _, err := os.Stat(filepath.Join(material, "index.json")); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "", caseIndex{}, err
		}
		return "", caseIndex{}, errors.New("index.json must be beside material, not inside its root")
	}
	entries, err := os.ReadDir(caseRoot)
	if err != nil {
		return "", caseIndex{}, err
	}
	if err := compareEntryNames(entries, []string{"index.json", "material"}); err != nil {
		return "", caseIndex{}, fmt.Errorf("%s/%s case files: %w", spec.provider, spec.name, err)
	}
	var index caseIndex
	if err := readJSONFile(indexPath, &index); err != nil {
		return "", caseIndex{}, err
	}
	if err := auditSensitiveTree(caseRoot); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateCaseIndex(index, spec, material); err != nil {
		return "", caseIndex{}, err
	}

	materialFiles, err := hashTree(material, "material.json")
	if err != nil {
		return "", caseIndex{}, err
	}
	var materialDocument materialArtifact
	if err := readJSONFile(filepath.Join(material, "material.json"), &materialDocument); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateMaterialDocument(materialDocument, index, materialFiles); err != nil {
		return "", caseIndex{}, err
	}
	if spec.materialID != "" && materialDocument.ID != spec.materialID {
		return "", caseIndex{}, errors.New("material ID differs from the registered case")
	}

	var plan capturePlan
	if err := readJSONFile(filepath.Join(material, "plan.json"), &plan); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateCapturePlan(plan, index, spec); err != nil {
		return "", caseIndex{}, err
	}

	var manifest captureManifest
	if err := readJSONFile(filepath.Join(material, "manifest.json"), &manifest); err != nil {
		return "", caseIndex{}, err
	}
	rawFiles, err := hashTree(filepath.Join(material, "raw"), "")
	if err != nil {
		return "", caseIndex{}, err
	}
	if err := validateCaptureManifest(manifest, index, plan, materialFiles, rawFiles); err != nil {
		return "", caseIndex{}, err
	}

	var raw rawManifest
	if err := readJSONFile(filepath.Join(material, "raw", "manifest.json"), &raw); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateRawManifest(raw, manifest, rawFiles); err != nil {
		return "", caseIndex{}, err
	}

	var report captureReport
	if err := readJSONFile(filepath.Join(material, "report.json"), &report); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateCaptureReport(report); err != nil {
		return "", caseIndex{}, err
	}
	if err := validateIndexFacts(index.Facts, spec, manifest); err != nil {
		return "", caseIndex{}, err
	}
	if spec.suite == codexRecordingSuite {
		if err := auditCodexCapture(material, "auto/deepseek-v4.1-flash-1017", index.Facts.SessionID); err != nil {
			return "", caseIndex{}, err
		}
	}
	return material, index, nil
}

func validateCaseIndex(index caseIndex, spec artifactCase, material string) error {
	if index.Provider != spec.provider {
		return fmt.Errorf("provider = %q, want %q", index.Provider, spec.provider)
	}
	if index.Case != spec.name {
		return fmt.Errorf("case = %q, want %q", index.Case, spec.name)
	}
	if index.Provenance != "consumer" {
		return fmt.Errorf("provenance = %q, want consumer", index.Provenance)
	}
	wantExport := filepath.ToSlash(filepath.Join(spec.casePath(), "material"))
	if index.ExportPath != wantExport {
		return fmt.Errorf("exportPath = %q, want %q", index.ExportPath, wantExport)
	}
	if index.Project != spec.flow.project {
		return fmt.Errorf("project = %q, want %q", index.Project, spec.flow.project)
	}
	if index.Prompt != spec.flow.prompt {
		return fmt.Errorf("prompt differs from selected %s/%s case", spec.provider, spec.name)
	}
	recordedAt, err := time.Parse(time.RFC3339, index.RecordedAt)
	if err != nil {
		return fmt.Errorf("recordedAt: %w", err)
	}
	if recordedAt.IsZero() {
		return errors.New("recordedAt is zero")
	}
	sdk, testify := spec.sdk, spec.testify
	if sdk == "" {
		sdk, testify = RecordingSDKVersion, RecordingTestifyVersion
	}
	if index.Versions["sdk"] != sdk {
		return fmt.Errorf("SDK version = %q, want recorded %q", index.Versions["sdk"], sdk)
	}
	if index.Versions["testify"] != testify {
		return fmt.Errorf("testify version = %q, want recorded %q", index.Versions["testify"], testify)
	}
	if index.Versions[spec.provider] == "" {
		return fmt.Errorf("%s CLI version is missing", spec.provider)
	}
	if !isHexDigest(index.Versions["mass"], 40) {
		return errors.New("MASS source hash is invalid")
	}
	cliPath := index.CodexPath
	if spec.provider == "claude" {
		cliPath = index.ClaudePath
	}
	if cliPath == "" || path.Base(cliPath) != cliPath {
		return fmt.Errorf("CLI identity must be an executable basename, got %q", cliPath)
	}
	for _, tool := range []string{spec.provider, "massd", "testify"} {
		if !isSHA256(index.ToolHashes[tool]) {
			return fmt.Errorf("toolSHA256.%s is missing or invalid", tool)
		}
	}
	if !isLocalRelative(index.SourceSession) || path.Base(index.SourceSession) != "session" {
		return fmt.Errorf("sourceSession must identify a scratch session directory, got %q", index.SourceSession)
	}
	required := []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl"}
	if spec.suite != "" {
		required = append(required, "raw/manifest.json", "raw/stderr.raw", "raw/transport.json")
	}
	allowed := map[string]bool{
		"plan.json": true, "material.json": true, "manifest.json": true,
		"raw/raw.jsonl": true, "raw/manifest.json": true, "raw/stderr.raw": true, "raw/transport.json": true,
	}
	for _, name := range required {
		if !isSHA256(index.Hashes[name]) {
			return fmt.Errorf("sha256.%s is missing or invalid", name)
		}
	}
	for name, digest := range index.Hashes {
		if !allowed[name] || !isLocalRelative(name) {
			return fmt.Errorf("unexpected sha256 entry %q", name)
		}
		if !isSHA256(digest) {
			return fmt.Errorf("sha256.%s is invalid", name)
		}
		actual, err := fileSHA256(filepath.Join(material, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("sha256.%s: %w", name, err)
		}
		if actual != digest {
			return fmt.Errorf("sha256.%s mismatch", name)
		}
	}
	return nil
}

func validateMaterialDocument(material materialArtifact, index caseIndex, files map[string]string) error {
	if material.ID == "" || path.Base(material.Path) != material.ID {
		return errors.New("material id/path identity is invalid")
	}
	if !filepath.IsAbs(material.Path) || !strings.HasSuffix(filepath.ToSlash(material.Path), "/"+index.SourceSession+"/materials/"+material.ID) {
		return fmt.Errorf("material path does not bind source session %q", index.SourceSession)
	}
	if material.Kind != "capture" || material.Action != "record" || material.Provenance != "consumer" {
		return fmt.Errorf("material identity is %q/%q/%q, want capture/record/consumer", material.Kind, material.Action, material.Provenance)
	}
	if !reflect.DeepEqual(material.Connections, []string{"main"}) {
		return fmt.Errorf("material connections = %v, want [main]", material.Connections)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, material.CreatedAt)
	if err != nil {
		return fmt.Errorf("material createdAt: %w", err)
	}
	recordedAt, err := time.Parse(time.RFC3339, index.RecordedAt)
	if err != nil {
		return err
	}
	if recordedAt.Before(createdAt) {
		return errors.New("index recordedAt precedes material creation")
	}
	if !maps.Equal(material.Files, files) {
		return fmt.Errorf("material file inventory differs from tree: declared=%d actual=%d", len(material.Files), len(files))
	}
	for _, name := range []string{"plan.json", "manifest.json", "report.json", "raw/manifest.json", "raw/raw.jsonl", "raw/stderr.raw", "raw/transport.json"} {
		if files[name] == "" {
			return fmt.Errorf("material is missing %s", name)
		}
	}
	if material.PlanSHA256 != index.Hashes["plan.json"] || material.PlanSHA256 != files["plan.json"] {
		return errors.New("material plan hash differs")
	}
	if material.ManifestSHA256 != index.Hashes["manifest.json"] || material.ManifestSHA256 != files["manifest.json"] {
		return errors.New("material manifest hash differs")
	}
	if material.RecordingSHA256 != index.Hashes["raw/raw.jsonl"] || material.RecordingSHA256 != files["raw/raw.jsonl"] {
		return errors.New("material raw recording hash differs")
	}
	return nil
}

func validateCapturePlan(plan capturePlan, index caseIndex, spec artifactCase) error {
	if plan.Version != 1 {
		return fmt.Errorf("plan version = %d", plan.Version)
	}
	if plan.AgentType != spec.provider {
		return fmt.Errorf("plan agentType = %q, want %q", plan.AgentType, spec.provider)
	}
	if plan.CaseID != "mass-"+spec.provider+"-"+spec.name {
		return fmt.Errorf("plan caseId = %q", plan.CaseID)
	}
	if plan.DriverProfile != "consumer" {
		return fmt.Errorf("plan driverProfile = %q, want consumer", plan.DriverProfile)
	}
	if plan.Project != spec.flow.project {
		return fmt.Errorf("plan project = %q, want %q", plan.Project, spec.flow.project)
	}
	if plan.CLIVersion == "" || plan.CLIVersion != index.Versions[spec.provider] {
		return fmt.Errorf("plan CLI version = %q, index = %q", plan.CLIVersion, index.Versions[spec.provider])
	}
	if plan.Model == "" {
		return errors.New("plan model is missing")
	}
	if spec.suite == codexRecordingSuite && plan.Model != "auto/deepseek-v4.1-flash-1017" {
		return errors.New("plan model differs from registered DeepSeek model")
	}
	return nil
}

func validateCaptureManifest(manifest captureManifest, index caseIndex, plan capturePlan, materialFiles, rawFiles map[string]string) error {
	if manifest.SchemaVersion != 1 || manifest.Status != "captured" || manifest.CaptureStatus != "complete" {
		return fmt.Errorf("capture manifest status is %d/%q/%q", manifest.SchemaVersion, manifest.Status, manifest.CaptureStatus)
	}
	if manifest.CredentialScan != "pass" || manifest.Provenance != "consumer" {
		return fmt.Errorf("capture manifest provenance/scan is %q/%q", manifest.Provenance, manifest.CredentialScan)
	}
	if err := validateRecordingFormat(manifest.RecordingFormat); err != nil {
		return err
	}
	if manifest.PlanSHA256 != index.Hashes["plan.json"] || manifest.PlanSHA256 != materialFiles["plan.json"] {
		return errors.New("manifest plan hash differs")
	}
	if manifest.RunSHA256 != materialFiles["raw/transport.json"] {
		return errors.New("manifest run hash differs from transport")
	}
	if !maps.Equal(manifest.Files, rawFiles) {
		return fmt.Errorf("manifest raw inventory differs: declared=%d actual=%d", len(manifest.Files), len(rawFiles))
	}
	for _, name := range []string{"manifest.json", "raw.jsonl", "stderr.raw", "transport.json"} {
		if manifest.Files[name] == "" {
			return fmt.Errorf("manifest is missing raw file %s", name)
		}
	}
	if len(manifest.SavedFiles) == 0 {
		return errors.New("manifest savedFiles is empty")
	}
	for name, digest := range manifest.SavedFiles {
		if !isLocalRelative(name) {
			return fmt.Errorf("unsafe saved file %q", name)
		}
		if actual := materialFiles[name]; actual == "" || actual != digest {
			return fmt.Errorf("saved file hash differs for %q", name)
		}
	}
	if manifest.Native.Version != plan.CLIVersion || manifest.Native.Version != index.Versions[plan.AgentType] {
		return errors.New("manifest native version differs")
	}
	if !filepath.IsAbs(manifest.Native.Executable) || !filepath.IsAbs(manifest.Native.CWD) || !isSHA256(manifest.Native.SHA256) {
		return errors.New("manifest native identity is invalid")
	}
	return nil
}

func validateRawManifest(raw rawManifest, manifest captureManifest, rawFiles map[string]string) error {
	if raw.Version != 1 || raw.CaptureStatus != "complete" {
		return fmt.Errorf("raw manifest status is %d/%q", raw.Version, raw.CaptureStatus)
	}
	if err := validateRecordingFormat(raw.RecordingFormat); err != nil {
		return err
	}
	payloadFiles := maps.Clone(rawFiles)
	delete(payloadFiles, "manifest.json")
	if !maps.Equal(raw.Files, payloadFiles) {
		return fmt.Errorf("raw manifest inventory differs: declared=%d actual=%d", len(raw.Files), len(payloadFiles))
	}
	if !reflect.DeepEqual(raw.Native, manifest.Native) || raw.ProxyVersion != manifest.ProxyVersion {
		return errors.New("raw and exported capture metadata differ")
	}
	return nil
}

func validateRecordingFormat(format recordingFormat) error {
	if format.Name != "agent-mock-jsonl" || format.Version != 1 || format.Transport != "stdio-jsonl" {
		return fmt.Errorf("unsupported recording format %+v", format)
	}
	return nil
}

func validateCaptureReport(report captureReport) error {
	if report.SchemaVersion != 1 || report.Status != "captured" || report.CaptureStatus != "complete" {
		return fmt.Errorf("capture report status is %d/%q/%q", report.SchemaVersion, report.Status, report.CaptureStatus)
	}
	if !report.Consumer || report.Provenance != "consumer" {
		return fmt.Errorf("capture report provenance is %v/%q", report.Consumer, report.Provenance)
	}
	if !report.NativeLaunched || !report.ClientEOF || !report.CleanupComplete {
		return fmt.Errorf("capture report lifecycle is native=%v eof=%v cleanup=%v", report.NativeLaunched, report.ClientEOF, report.CleanupComplete)
	}
	return nil
}

func validateIndexFacts(facts facts, spec artifactCase, manifest captureManifest) error {
	if facts.SessionID == "" || facts.State != "idle" || !facts.Accepted {
		return fmt.Errorf("index facts status is %q/%q/%v", facts.SessionID, facts.State, facts.Accepted)
	}
	foundHistory := false
	for name := range manifest.SavedFiles {
		if strings.Contains(path.Base(name), facts.SessionID) {
			foundHistory = true
			break
		}
	}
	if !foundHistory {
		return errors.New("index session does not bind saved native history")
	}
	if len(facts.Events) == 0 {
		return errors.New("index has no public events")
	}
	last := facts.Events[len(facts.Events)-1]
	if last.Kind != "agent.message" || last.Text == "" {
		return errors.New("index does not end with a public assistant message")
	}
	if spec.provider == "claude" {
		if facts.Input == nil || !facts.Input.Sent {
			return errors.New("claude input receipt is missing")
		}
		if facts.Input.NativeAccepted != nil {
			return errors.New("claude input must not claim invented native acceptance")
		}
	} else if facts.Input != nil {
		return errors.New("codex index has an unexpected Claude input receipt")
	}
	if !spec.flow.expectPermission {
		if facts.Permission != nil {
			return errors.New("basic case contains a permission fact")
		}
		return nil
	}
	permission := facts.Permission
	if permission == nil || permission.Request.Kind != "permission" {
		return errors.New("permission fact is missing")
	}
	if permission.Interaction == "" || permission.Request.ToolCallID == "" || len(permission.Request.Options) == 0 {
		return errors.New("permission request identity/content is incomplete")
	}
	if spec.suite == "" && len(permission.Request.Content) == 0 {
		return errors.New("historical permission request content is missing")
	}
	if spec.suite == codexRecordingSuite {
		call, result := false, false
		for _, event := range facts.Events {
			if event.ToolCallID != permission.Request.ToolCallID {
				continue
			}
			call = call || event.Kind == "agent.tool_call" && strings.Contains(event.Text, "printf mass-approved > permission-proof.txt")
			result = result || event.Kind == "agent.tool_result" && event.Status == "completed" && event.ExitCode != nil && *event.ExitCode == 0
		}
		if !call || !result || !permission.Closed {
			return errors.New("permission lacks linked command, successful result or closure")
		}
	}
	if permission.StateBeforeAnswer != "requires_action" || permission.StateAfterAnswer == "" || permission.StateAfterAnswer == "requires_action" || !permission.Completed {
		return errors.New("permission state/completion fact is invalid")
	}
	if !permission.Answer.Sent || permission.Answer.NativeAccepted != nil {
		return errors.New("permission answer evidence is invalid")
	}
	offered := false
	for _, option := range permission.Request.Options {
		if option.OptionID == permission.Answer.OptionID && string(option.Kind) == "allow_once" {
			offered = true
		}
	}
	if !offered {
		return errors.New("permission answer did not select an offered allow-once option")
	}
	return nil
}

func hashTree(root, skip string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular material file %q", filePath)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == skip {
			return nil
		}
		digest, err := fileSHA256(filePath)
		if err != nil {
			return err
		}
		files[relative] = digest
		return nil
	})
	return files, err
}

func isSHA256(value string) bool {
	return isHexDigest(value, sha256.Size*2)
}

func isHexDigest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isLocalRelative(name string) bool {
	return name != "" && name == path.Clean(name) && !path.IsAbs(name) && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

var credentialPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"bearer token", regexp.MustCompile(`(?i)\bauthorization\b["']?\s*[:=]\s*["']?bearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	{"credential value", regexp.MustCompile(`(?i)"?(?:access_token|refresh_token|id_token|auth_token|api_key|client_secret|password)"?\s*[:=]\s*"(?:[^"\\]|\\.){8,}"`)},
	{"environment token", regexp.MustCompile(`(?i)\b(?:ANTHROPIC_AUTH_TOKEN|ANTHROPIC_API_KEY|OPENAI_API_KEY|OPENAI_ACCESS_TOKEN)\s*=\s*\S{8,}`)},
	{"Anthropic token", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}`)},
	{"OpenAI token", regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{"private key", regexp.MustCompile(`-{5}BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-{5}`)},
}

func auditSensitiveTree(root string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		lowerName := strings.ToLower(entry.Name())
		switch lowerName {
		case "auth.json", ".credentials.json", "id_rsa", "id_ed25519":
			return fmt.Errorf("credential-bearing file %q", filePath)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		if home != "" && (strings.Contains(string(data), home) || strings.Contains(string(data), "/Users/")) {
			return fmt.Errorf("personal directory in %q", filePath)
		}
		for _, pattern := range credentialPatterns {
			if pattern.re.Match(data) {
				return fmt.Errorf("%s in %q", pattern.name, filePath)
			}
		}
		return nil
	})
}

func rewriteJSONFile(t *testing.T, filePath string, mutate func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))
	mutate(value)
	data, err = json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, append(data, '\n'), 0o600))
}

func resignRecordingCase(t *testing.T, caseRoot string) {
	t.Helper()
	material := filepath.Join(caseRoot, "material")
	files, err := hashTree(material, "material.json")
	require.NoError(t, err)
	var document materialArtifact
	readJSON(t, filepath.Join(material, "material.json"), &document)
	document.Files = files
	document.PlanSHA256 = files["plan.json"]
	document.ManifestSHA256 = files["manifest.json"]
	document.RecordingSHA256 = files["raw/raw.jsonl"]
	writeJSON(t, filepath.Join(material, "material.json"), document)
	materialHash, err := fileSHA256(filepath.Join(material, "material.json"))
	require.NoError(t, err)
	var index caseIndex
	readJSON(t, filepath.Join(caseRoot, "index.json"), &index)
	for name := range index.Hashes {
		index.Hashes[name] = files[name]
	}
	index.Hashes["material.json"] = materialHash
	writeJSON(t, filepath.Join(caseRoot, "index.json"), index)
}

func copyRecordingCase(t *testing.T, root string, spec artifactCase) (string, string) {
	t.Helper()
	source := filepath.Join(root, spec.casePath())
	destinationRoot := t.TempDir()
	destination := filepath.Join(destinationRoot, spec.casePath())
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
	require.NoError(t, os.CopyFS(destination, os.DirFS(source)))
	return destinationRoot, destination
}

func RunAuditRejectsInvalidCaseSelection(t *testing.T) {
	for _, suite := range []string{"recordings", codexRecordingSuite} {
		for _, mutation := range []string{"missing case", "extra case", "extra provider", "extra file", "missing suite", "extra suite"} {
			t.Run(suite+"/"+mutation, func(t *testing.T) {
				root := t.TempDir()
				for _, spec := range allRecordingArtifacts() {
					require.NoError(t, os.MkdirAll(filepath.Join(root, spec.casePath()), 0o700))
				}
				require.NoError(t, validateRecordingCaseSelection(root))
				simulation := filepath.Join(root, "tests", "testdata", "simulation")
				selected := filepath.Join(simulation, suite)
				switch mutation {
				case "missing case":
					require.NoError(t, os.Remove(filepath.Join(selected, "codex", "permission")))
				case "extra case":
					require.NoError(t, os.MkdirAll(filepath.Join(selected, "codex", "extra"), 0o700))
				case "extra provider":
					require.NoError(t, os.MkdirAll(filepath.Join(selected, "unknown"), 0o700))
				case "extra file":
					require.NoError(t, os.WriteFile(filepath.Join(selected, "unexpected.json"), []byte("{}"), 0o600))
				case "missing suite":
					require.NoError(t, os.RemoveAll(selected))
				case "extra suite":
					require.NoError(t, os.MkdirAll(filepath.Join(simulation, "unregistered"), 0o700))
				}
				require.Error(t, validateRecordingCaseSelection(root))
			})
		}
	}
}

func RunAuditRejectsInvalidArtifacts(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, spec := range []artifactCase{recordingArtifacts[0], currentCodexArtifacts[0]} {
		t.Run(spec.directory(), func(t *testing.T) {
			runAuditRejectsInvalidArtifacts(t, root, spec)
		})
	}
}

func runAuditRejectsInvalidArtifacts(t *testing.T, root string, spec artifactCase) {
	tests := []struct {
		name   string
		want   string
		resign bool
		mutate func(*testing.T, string)
	}{
		{
			name: "missing index",
			want: "index.json",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "index.json")))
			},
		},
		{
			name: "index inside material",
			want: "beside material",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Rename(filepath.Join(caseRoot, "index.json"), filepath.Join(caseRoot, "material", "index.json")))
			},
		},
		{
			name: "wrong provider",
			want: "provider",
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) { value["provider"] = "claude" })
			},
		},
		{
			name: "wrong case",
			want: "case",
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) { value["case"] = "other" })
			},
		},
		{
			name: "wrong provenance",
			want: "provenance",
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) { value["provenance"] = "official" })
			},
		},
		{
			name: "wrong export path",
			want: "exportPath",
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) { value["exportPath"] = "material" })
			},
		},
		{
			name: "missing plan",
			want: "plan.json",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "material", "plan.json")))
			},
		},
		{
			name: "missing material metadata",
			want: "material.json",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "material", "material.json")))
			},
		},
		{
			name: "missing manifest",
			want: "manifest.json",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "material", "manifest.json")))
			},
		},
		{
			name: "missing raw",
			want: "raw.jsonl",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "material", "raw", "raw.jsonl")))
			},
		},
		{
			name: "raw hash mismatch",
			want: "mismatch",
			mutate: func(t *testing.T, caseRoot string) {
				filePath := filepath.Join(caseRoot, "material", "raw", "raw.jsonl")
				require.NoError(t, os.WriteFile(filePath, []byte("{}\n"), 0o600))
			},
		},
		{
			name: "material inventory missing file",
			want: "inventory",
			mutate: func(t *testing.T, caseRoot string) {
				manifest := filepath.Join(caseRoot, "material", "manifest.json")
				var value struct {
					SavedFiles map[string]string `json:"savedFiles"`
				}
				readJSON(t, manifest, &value)
				var saved string
				for name := range value.SavedFiles {
					saved = name
					break
				}
				require.NotEmpty(t, saved)
				require.NoError(t, os.Remove(filepath.Join(caseRoot, "material", filepath.FromSlash(saved))))
			},
		},
		{
			name: "material inventory extra file",
			want: "inventory",
			mutate: func(t *testing.T, caseRoot string) {
				require.NoError(t, os.WriteFile(filepath.Join(caseRoot, "material", "unexpected.txt"), []byte("unexpected\n"), 0o600))
			},
		},
		{
			name:   "plan provider mismatch",
			want:   "agentType",
			resign: true,
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "plan.json"), func(value map[string]any) { value["agentType"] = "claude" })
			},
		},
		{
			name:   "manifest plan mismatch",
			want:   "manifest plan hash",
			resign: true,
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "manifest.json"), func(value map[string]any) { value["planSHA256"] = strings.Repeat("0", 64) })
			},
		},
		{
			name:   "raw manifest capture mismatch",
			want:   "raw manifest status",
			resign: true,
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "raw", "manifest.json"), func(value map[string]any) { value["captureStatus"] = "failed" })
				rawManifestHash, err := fileSHA256(filepath.Join(caseRoot, "material", "raw", "manifest.json"))
				require.NoError(t, err)
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "manifest.json"), func(value map[string]any) {
					value["files"].(map[string]any)["manifest.json"] = rawManifestHash
				})
			},
		},
		{
			name:   "report lifecycle mismatch",
			want:   "capture report lifecycle",
			resign: true,
			mutate: func(t *testing.T, caseRoot string) {
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "report.json"), func(value map[string]any) { value["clientEOF"] = false })
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destinationRoot, caseRoot := copyRecordingCase(t, root, spec)
			test.mutate(t, caseRoot)
			if test.resign {
				resignRecordingCase(t, caseRoot)
			}
			_, _, err := auditRecordingCase(destinationRoot, spec)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func RunAuditRejectsSensitiveMaterial(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "material.json")
	require.NoError(t, os.WriteFile(filePath, []byte(`{"tokenSource":"ANTHROPIC_AUTH_TOKEN"}`), 0o600))
	require.NoError(t, auditSensitiveTree(root))
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "home", content: "/Users/example/private"},
		{name: "token", content: "ANTHROPIC_AUTH_TOKEN=not-a-real-secret-value"},
		{name: "bearer", content: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz"},
		{name: "private key", content: "-----BEGIN PRIVATE KEY-----"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filePath, []byte(test.content), 0o600))
			require.Error(t, auditSensitiveTree(root))
		})
	}
}

func RunRejectsIncompleteExecution(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "codex", "basic", project, prompt)
	session := h.replay(material, index.Facts, flowCase{name: "basic", provider: "codex", prompt: index.Prompt, project: index.Project})
	t.Logf("validated active Codex baseline before execution mutations: %s", index.ExportPath)
	pristine := filepath.Join(t.TempDir(), "session")
	restoreDirectory(t, session, pristine)
	for _, test := range []struct {
		name   string
		want   string
		mutate func(*testing.T, string)
	}{
		{
			name: "missing execution index",
			want: "index.json",
			mutate: func(t *testing.T, session string) {
				require.NoError(t, os.Remove(filepath.Join(session, "executions", "index.json")))
			},
		},
		{
			name: "failed execution index status",
			want: "execution status",
			mutate: func(t *testing.T, session string) {
				rewriteJSONFile(t, filepath.Join(session, "executions", "index.json"), func(value map[string]any) {
					value["executions"].([]any)[0].(map[string]any)["status"] = "failed"
				})
			},
		},
		{
			name: "incomplete connection index",
			want: "connection inventory differs from index",
			mutate: func(t *testing.T, session string) {
				rewriteJSONFile(t, filepath.Join(session, "executions", "index.json"), func(value map[string]any) {
					value["executions"].([]any)[0].(map[string]any)["connections"] = []any{}
				})
			},
		},
		{
			name: "descriptor hash mismatch",
			want: "descriptor differs from index",
			mutate: func(t *testing.T, session string) {
				descriptor, _, _ := executionEvidencePaths(t, session)
				require.NoError(t, os.WriteFile(descriptor, append(mustReadFile(t, descriptor), '\n'), 0o600))
			},
		},
		{
			name: "missing process receipt",
			want: "main.process.json",
			mutate: func(t *testing.T, session string) {
				_, _, process := executionEvidencePaths(t, session)
				require.NoError(t, os.Remove(process))
			},
		},
		{
			name: "failed process receipt",
			want: "mock process receipt",
			mutate: func(t *testing.T, session string) {
				_, _, process := executionEvidencePaths(t, session)
				rewriteJSONFile(t, process, func(value map[string]any) { value["status"] = "failed" })
			},
		},
		{
			name: "missing replay receipt",
			want: "receipt",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				require.NoError(t, os.Remove(receipt))
			},
		},
		{
			name: "failed replay receipt",
			want: "mock replay receipt status",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["status"] = "failed" })
			},
		},
		{
			name: "incomplete consumption",
			want: "full consumption",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["fullConsumption"] = false })
			},
		},
		{
			name: "missing client EOF",
			want: "client EOF",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["clientEOF"] = false })
			},
		},
		{
			name: "native launched",
			want: "does not prove an in-process mock",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["nativeLaunched"] = true })
			},
		},
		{
			name: "incomplete cleanup",
			want: "cleanup",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["cleanupComplete"] = false })
			},
		},
		{
			name: "receipt recording mismatch",
			want: "recording hash differs",
			mutate: func(t *testing.T, session string) {
				_, receipt, _ := executionEvidencePaths(t, session)
				rewriteJSONFile(t, receipt, func(value map[string]any) { value["recordingSHA256"] = strings.Repeat("0", 64) })
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			restoreDirectory(t, pristine, session)
			test.mutate(t, session)
			require.ErrorContains(t, verifyExecution(session, material), test.want)
		})
	}
}

func restoreDirectory(t *testing.T, source, destination string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(destination))
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
	require.NoError(t, os.CopyFS(destination, os.DirFS(source)))
}

func executionEvidencePaths(t *testing.T, session string) (string, string, string) {
	t.Helper()
	indexPath := filepath.Join(session, "executions", "index.json")
	var index struct {
		Executions []struct {
			ID string `json:"id"`
		} `json:"executions"`
	}
	readJSON(t, indexPath, &index)
	require.Len(t, index.Executions, 1)
	fixture, err := agenttestify.Open(session)
	require.NoError(t, err)
	report, err := fixture.ExecutionReport(index.Executions[0].ID)
	require.NoError(t, err)
	require.Len(t, report.Connections, 1)
	return report.ExecutionPath, report.Connections[0].ReceiptPath, report.Connections[0].ProcessReceiptPath
}

func mustReadFile(t *testing.T, filePath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	return data
}
