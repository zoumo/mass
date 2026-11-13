package recordingtest

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	codexRecordingSuite  = "recordings-4959190bc518"
	claudeRecordingSuite = "recordings-09d4ddf1ff20"
)

var currentCodexArtifacts = []artifactCase{
	{
		provider: "codex", name: "basic", suite: codexRecordingSuite,
		sdk: "v0.0.0-20261111031457-4959190bc518", testify: "v0.1.3-0.20261111025559-1f8db14a50e1",
		materialID: "50194530555049c8e6a258fa3b85ebb5",
		flow:       flowCase{name: "basic", provider: "codex", prompt: prompt, project: "/var/tmp/mass-record-codex-3589059373/project"},
	},
	{
		provider: "codex", name: "permission", suite: codexRecordingSuite,
		sdk: "v0.0.0-20261111031457-4959190bc518", testify: "v0.1.3-0.20261111025559-1f8db14a50e1",
		materialID: "a172abf87285e44f5d82f74a258d9fff",
		flow:       flowCase{name: "permission", provider: "codex", prompt: permissionPrompt, project: "/var/tmp/mass-record-codex-permission-1081652419/project", expectPermission: true, expectedPermission: "Approve command execution"},
	},
}

var currentClaudeArtifacts = []artifactCase{
	{
		provider: "claude", name: "basic", suite: claudeRecordingSuite,
		sdk: RuntimeSDKVersion, testify: TestifyVersion,
		materialID: "5cf2bc799cde1a0f60ed6706ca36ef15",
		flow:       flowCase{name: "basic", provider: "claude", prompt: claudePrompt, project: "/var/tmp/mass-record-claude-153315685/project", initialInput: true, ignoreStreamKinds: true},
	},
	{
		provider: "claude", name: "permission", suite: claudeRecordingSuite,
		sdk: RuntimeSDKVersion, testify: TestifyVersion,
		materialID: "21e27325e989edaf20923eade3fada20",
		flow:       flowCase{name: "permission", provider: "claude", prompt: claudePermissionPrompt, project: "/var/tmp/mass-record-claude-permission-3946514327/project", initialInput: true, ignoreStreamKinds: true, expectPermission: true},
	},
}

var activeRecordingArtifacts = []artifactCase{
	currentCodexArtifacts[0],
	currentCodexArtifacts[1],
	currentClaudeArtifacts[0],
	currentClaudeArtifacts[1],
}

func allRecordingArtifacts() []artifactCase {
	all := append([]artifactCase{}, recordingArtifacts...)
	all = append(all, currentCodexArtifacts...)
	return append(all, currentClaudeArtifacts...)
}

func (spec artifactCase) directory() string {
	if spec.suite == "" {
		return "recordings"
	}
	return spec.suite
}

func (spec artifactCase) casePath() string {
	return filepath.Join("tests", "testdata", "simulation", spec.directory(), spec.provider, spec.name)
}

func selectedRecordingCase(provider, name string) (artifactCase, error) {
	for _, spec := range activeRecordingArtifacts {
		if spec.provider == provider && spec.name == name {
			return spec, nil
		}
	}
	return artifactCase{}, fmt.Errorf("no active recording for %s/%s", provider, name)
}

func validateActiveRecordingSelection(selected []artifactCase) error {
	if len(selected) != len(activeRecordingArtifacts) {
		return fmt.Errorf("active selection contains %d cases, want %d", len(selected), len(activeRecordingArtifacts))
	}
	seen := map[string]bool{}
	for _, spec := range selected {
		key := spec.provider + "/" + spec.name
		if seen[key] {
			return fmt.Errorf("duplicate active recording %s", key)
		}
		seen[key] = true
		expected, err := selectedRecordingCase(spec.provider, spec.name)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(spec, expected) {
			return fmt.Errorf("active material/version differs for %s", key)
		}
	}
	return nil
}

func RunAuditRejectsInvalidRegistry(t *testing.T) {
	require.NoError(t, validateActiveRecordingSelection(activeRecordingArtifacts))
	for _, mutation := range []string{"missing", "extra", "duplicate", "historical Codex", "wrong provider", "wrong case", "wrong suite", "wrong material", "wrong SDK", "wrong testify"} {
		t.Run(mutation, func(t *testing.T) {
			selected := append([]artifactCase{}, activeRecordingArtifacts...)
			switch mutation {
			case "missing":
				selected = selected[:len(selected)-1]
			case "extra":
				selected = append(selected, selected[0])
			case "duplicate":
				selected[1] = selected[0]
			case "historical Codex":
				selected[0] = recordingArtifacts[0]
			case "wrong provider":
				selected[0].provider = "unknown"
			case "wrong case":
				selected[0].name = "unknown"
			case "wrong suite":
				selected[0].suite = "recordings"
			case "wrong material":
				selected[0].materialID = "wrong"
			case "wrong SDK":
				selected[0].sdk = RecordingSDKVersion
			case "wrong testify":
				selected[0].testify = RecordingTestifyVersion
			}
			require.Error(t, validateActiveRecordingSelection(selected))
		})
	}
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, mutation := range []string{"SDK", "testify", "model", "material", "raw manifest hash", "closure", "tool identity", "tool result"} {
		t.Run("material/"+mutation, func(t *testing.T) {
			spec := currentCodexArtifacts[1]
			destinationRoot, caseRoot := copyRecordingCase(t, root, spec)
			switch mutation {
			case "SDK", "testify":
				key := strings.ToLower(mutation)
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) {
					value["versions"].(map[string]any)[key] = "wrong"
				})
			case "model":
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "plan.json"), func(value map[string]any) {
					value["model"] = "gpt-6.1-sol"
				})
				resignRecordingCase(t, caseRoot)
			case "material":
				rewriteJSONFile(t, filepath.Join(caseRoot, "material", "material.json"), func(value map[string]any) {
					value["id"] = "wrong"
				})
				resignRecordingCase(t, caseRoot)
			case "raw manifest hash":
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) {
					delete(value["sha256"].(map[string]any), "raw/manifest.json")
				})
			case "closure", "tool identity", "tool result":
				rewriteJSONFile(t, filepath.Join(caseRoot, "index.json"), func(value map[string]any) {
					facts := value["facts"].(map[string]any)
					if mutation == "closure" {
						facts["permission"].(map[string]any)["closed"] = false
						return
					}
					for _, event := range facts["events"].([]any) {
						fact := event.(map[string]any)
						if mutation == "tool identity" && fact["kind"] == "agent.tool_call" {
							fact["toolCallId"] = "wrong"
						}
						if mutation == "tool result" && fact["kind"] == "agent.tool_result" {
							fact["exitCode"] = 1
						}
					}
				})
			}
			_, _, err := auditRecordingCase(destinationRoot, spec)
			require.Error(t, err)
		})
	}
}
