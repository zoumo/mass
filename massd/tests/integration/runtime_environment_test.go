package integration_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/metastore/backend"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

func (s *entrypointSuite) TestCodexRuntimeFileEnvironment() {
	scenario, work := nativeScenario(s.T(), "codex", "lifecycle-create")
	s.runtimeFileEnvironment("codex", mock, scenario, work)
}

func (s *entrypointSuite) TestClaudeRuntimeFileEnvironment() {
	scenario, work := nativeScenario(s.T(), "claude", "question")
	s.runtimeFileEnvironment("claude", claudeMock, scenario, work)
}

func (s *entrypointSuite) runtimeFileEnvironment(kind, peer, scenario, work string) {
	s.T().Helper()
	dir := s.T().TempDir()
	marker := []byte("mass-runtime-" + uuid.NewString() + "${OPAQUE}")
	source := filepath.Join(dir, "source.env")
	defaults := filepath.Join(dir, "defaults.env")
	overrides := filepath.Join(dir, "overrides.env")
	evidence := filepath.Join(dir, "verified")
	launcher := filepath.Join(dir, "launch")
	configDir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(defaults, []byte("MASS_IMPORTED='first'\nMASS_OVERRIDE='from-file'\nCLAUDE_CONFIG_DIR='"+configDir+"'\n"), 0o600))
	s.Require().NoError(os.WriteFile(overrides, []byte("MASS_IMPORTED='second'\nMASS_IMPORTED_MARKER='"+string(marker)+"'\n"), 0o600))
	s.Require().NoError(os.WriteFile(launcher, []byte(`#!/bin/sh
printf 'launched\n' > "$MASS_EVIDENCE" || exit 69
IFS= read -r assignment < "$MASS_SOURCE" || exit 70
expected=${assignment#*\'}
expected=${expected%\'}
[ "$MASS_RUNTIME_MARKER" = "$expected" ] || exit 71
[ "$MASS_IMPORTED_MARKER" = "$expected" ] || exit 72
[ "$MASS_IMPORTED" = second ] || exit 73
[ "${MASS_OVERRIDE-unset}" = '' ] || exit 74
printf 'verified\n' > "$MASS_EVIDENCE" || exit 75
exec "$MASS_AGENT" "$@"
`), 0o700))

	const name = "file-env"
	r := resource.Runtime{
		Metadata: resource.Metadata{Name: name},
		Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{
			"executable": launcher, "envPolicy": "replace", "scenario": scenario,
			"env": []any{
				map[string]any{"name": "MASS_SOURCE", "value": source},
				map[string]any{"name": "MASS_EVIDENCE", "value": evidence},
				map[string]any{"name": "MASS_AGENT", "value": peer},
				map[string]any{"name": "MASS_OVERRIDE", "value": ""},
				map[string]any{"name": "MASS_RUNTIME_MARKER", "valueFrom": map[string]any{
					"fileKeyRef": map[string]any{"path": source, "key": "SOURCE_TOKEN"},
				}},
			},
			"envFrom": []any{
				map[string]any{"fileRef": map[string]any{"path": defaults}},
				map[string]any{"fileRef": map[string]any{"path": overrides}},
			},
		}},
	}
	s.call("runtime.create", workflowParams{Runtime: &r}, nil)
	s.call("runtime.update", workflowParams{Name: name, Spec: &r.Spec}, nil)

	ref := json.RawMessage(`{"runtimeRef":"file-env"}`)
	for _, content := range []string{
		"OTHER_KEY='" + string(marker) + "'\n",
		"SOURCE_TOKEN=\"" + string(marker) + "\"\n",
	} {
		s.Require().NoError(os.WriteFile(source, []byte(content), 0o600))
		response := rawRPCCall(s.T(), s.url, "", "runtime.start", ref)
		s.Require().NotNil(response.Error)
		s.Require().Empty(response.Result)
		s.Require().False(bytes.Contains([]byte(response.Error.Message), marker), "start error message contains a file value")
		s.Require().False(bytes.Contains(response.Error.Data, marker), "start error data contains a file value")
		var operation runtime.OperationError
		s.Require().NoError(json.Unmarshal(response.Error.Data, &operation))
		s.Require().Equal("runtime_environment_invalid", operation.Reason)
		_, err := os.Stat(evidence)
		s.Require().ErrorIs(err, fs.ErrNotExist)
	}

	s.Require().NoError(os.WriteFile(source, []byte("SOURCE_TOKEN='"+string(marker)+"'\n"), 0o600))
	var started resource.Runtime
	s.call("runtime.start", workflowParams{Name: name}, &started)
	s.Require().Equal("running", started.Status.State)

	// Claude opens its process at Session creation. Codex already runs its
	// app-server after Runtime start.
	if kind == "claude" {
		create := resource.Session{
			Metadata: resource.Metadata{Namespace: "demo", Name: "file-env"},
			Spec:     resource.SessionSpec{RuntimeRef: name, SessionSpec: runtime.SessionSpec{Cwd: work}},
		}
		var session resource.Session
		s.call("session.create", workflowParams{Create: &create}, &session)
		s.Require().NotEmpty(session.Status.SessionID)
		s.call("session.close", workflowParams{Session: resource.SessionRef{SessionID: session.Status.SessionID}}, nil)
	}
	result, err := os.ReadFile(evidence)
	s.Require().NoError(err)
	s.Require().Equal("verified\n", string(result))

	for _, call := range []struct {
		method string
		params json.RawMessage
	}{
		{"runtime.get", ref},
		{"runtime.list", json.RawMessage(`{}`)},
	} {
		response := rawRPCCall(s.T(), s.url, "", call.method, call.params)
		s.Require().Nil(response.Error)
		s.Require().False(bytes.Contains(response.Result, marker), "Runtime read contains the resolved value")
		s.Require().Contains(string(response.Result), "fileKeyRef")
		s.Require().Contains(string(response.Result), "envFrom")
	}
	s.call("runtime.stop", workflowParams{Name: name}, nil)
	meta, err := backend.Open(s.data, metastore.Config{Backend: s.backend}, true)
	s.Require().NoError(err)
	defer func() { s.NoError(meta.Close()) }()
	stored, err := meta.GetRuntime(name)
	s.Require().NoError(err)
	encoded, err := json.Marshal(stored.Spec.Config)
	s.Require().NoError(err)
	s.Require().False(bytes.Contains(encoded, marker), "MetaStore Runtime contains the resolved value")
	declared, err := json.Marshal(r.Spec.Config)
	s.Require().NoError(err)
	s.Require().True(bytes.Equal(declared, encoded), "MetaStore configuration differs from the declared references")
	s.Require().NoError(filepath.WalkDir(s.data, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s.Require().False(bytes.Contains(content, marker), "stored data contains the resolved value: %s", path)
		return nil
	}))
}
