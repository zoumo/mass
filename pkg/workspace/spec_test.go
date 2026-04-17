package workspace_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/pkg/workspace"
)

// validWorkspaceSpec returns a WorkspaceSpec that passes all validation rules.
func validWorkspaceSpec() workspace.WorkspaceSpec {
	return workspace.WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata: workspace.WorkspaceMetadata{
			Name: "test-workspace",
		},
		Source: workspace.Source{
			Type: workspace.SourceTypeGit,
			Git: workspace.GitSource{
				URL: "https://github.com/org/project.git",
				Ref: "main",
			},
		},
	}
}

// ---- suite ----

type SpecSuite struct {
	suite.Suite
}

func (s *SpecSuite) TestParseValid() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "test-workspace"},
		"source": {"type": "git", "url": "https://github.com/org/project.git"}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal("test-workspace", spec.Metadata.Name)
	s.Equal(workspace.SourceTypeGit, spec.Source.Type)
	s.Equal("https://github.com/org/project.git", spec.Source.Git.URL)
}

func (s *SpecSuite) TestParseMalformedJSON() {
	data := []byte(`{not json}`)
	_, err := workspace.ParseWorkspaceSpec(data)
	s.Require().Error(err)
	s.Contains(err.Error(), "parse spec")
}

func (s *SpecSuite) TestParseGitSource() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "git-project"},
		"source": {
			"type": "git",
			"url": "https://github.com/org/repo.git",
			"ref": "feature/auth",
			"depth": 1
		}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal(workspace.SourceTypeGit, spec.Source.Type)
	s.Equal("https://github.com/org/repo.git", spec.Source.Git.URL)
	s.Equal("feature/auth", spec.Source.Git.Ref)
	s.Equal(1, spec.Source.Git.Depth)
}

func (s *SpecSuite) TestParseEmptyDirSource() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "empty-project"},
		"source": {"type": "emptyDir"}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal(workspace.SourceTypeEmptyDir, spec.Source.Type)
}

func (s *SpecSuite) TestParseLocalSource() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "local-project"},
		"source": {"type": "local", "path": "/home/user/project"}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal(workspace.SourceTypeLocal, spec.Source.Type)
	s.Equal("/home/user/project", spec.Source.Local.Path)
}

func (s *SpecSuite) TestParseHooks() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "with-hooks"},
		"source": {"type": "git", "url": "https://github.com/org/repo.git"},
		"hooks": {
			"setup": [
				{"command": "npm", "args": ["install"], "description": "Install deps"}
			],
			"teardown": [
				{"command": "docker", "args": ["compose", "down"]}
			]
		}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Len(spec.Hooks.Setup, 1)
	s.Equal("npm", spec.Hooks.Setup[0].Command)
	s.Equal([]string{"install"}, spec.Hooks.Setup[0].Args)
	s.Equal("Install deps", spec.Hooks.Setup[0].Description)
	s.Len(spec.Hooks.Teardown, 1)
	s.Equal("docker", spec.Hooks.Teardown[0].Command)
}

func (s *SpecSuite) TestParseUnknownSourceType() {
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "bad-source"},
		"source": {"type": "invalid"}
	}`)
	_, err := workspace.ParseWorkspaceSpec(data)
	s.Require().Error(err)
	s.Contains(err.Error(), "unknown source type")
}

func (s *SpecSuite) TestMarshalGitSource() {
	spec := workspace.WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    workspace.WorkspaceMetadata{Name: "git-project"},
		Source: workspace.Source{
			Type: workspace.SourceTypeGit,
			Git: workspace.GitSource{
				URL:   "https://github.com/org/repo.git",
				Ref:   "main",
				Depth: 1,
			},
		},
	}
	data, err := json.Marshal(spec)
	s.Require().NoError(err)

	// Unmarshal to verify round-trip.
	var parsed workspace.WorkspaceSpec
	s.Require().NoError(json.Unmarshal(data, &parsed))
	s.Equal(spec.MassVersion, parsed.MassVersion)
	s.Equal(spec.Metadata.Name, parsed.Metadata.Name)
	s.Equal(spec.Source.Type, parsed.Source.Type)
	s.Equal(spec.Source.Git.URL, parsed.Source.Git.URL)
	s.Equal(spec.Source.Git.Ref, parsed.Source.Git.Ref)
	s.Equal(spec.Source.Git.Depth, parsed.Source.Git.Depth)
}

func (s *SpecSuite) TestMarshalEmptyDirSource() {
	spec := workspace.WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    workspace.WorkspaceMetadata{Name: "empty-project"},
		Source: workspace.Source{
			Type: workspace.SourceTypeEmptyDir,
		},
	}
	data, err := json.Marshal(spec)
	s.Require().NoError(err)

	// Verify the JSON contains only type field for emptyDir source.
	var raw map[string]interface{}
	s.Require().NoError(json.Unmarshal(data, &raw))
	source := raw["source"].(map[string]interface{})
	s.Equal("emptyDir", source["type"])
	s.Len(source, 1) // Only type field

	// Round-trip.
	var parsed workspace.WorkspaceSpec
	s.Require().NoError(json.Unmarshal(data, &parsed))
	s.Equal(workspace.SourceTypeEmptyDir, parsed.Source.Type)
}

func (s *SpecSuite) TestMarshalLocalSource() {
	spec := workspace.WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    workspace.WorkspaceMetadata{Name: "local-project"},
		Source: workspace.Source{
			Type: workspace.SourceTypeLocal,
			Local: workspace.LocalSource{
				Path: "/home/user/project",
			},
		},
	}
	data, err := json.Marshal(spec)
	s.Require().NoError(err)

	// Round-trip.
	var parsed workspace.WorkspaceSpec
	s.Require().NoError(json.Unmarshal(data, &parsed))
	s.Equal(workspace.SourceTypeLocal, parsed.Source.Type)
	s.Equal("/home/user/project", parsed.Source.Local.Path)
}

func (s *SpecSuite) TestValidateValid() {
	s.NoError(workspace.ValidateWorkspaceSpec(validWorkspaceSpec()))
}

func (s *SpecSuite) TestValidateMissingMassVersion() {
	spec := validWorkspaceSpec()
	spec.MassVersion = ""
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "massVersion")
}

func (s *SpecSuite) TestValidateUnknownMajorVersion() {
	spec := validWorkspaceSpec()
	spec.MassVersion = "1.0.0" // major == 1, unsupported
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "major")
}

func (s *SpecSuite) TestValidateInvalidSemVer() {
	spec := validWorkspaceSpec()
	spec.MassVersion = "not-semver"
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "SemVer")
}

func (s *SpecSuite) TestValidateMissingMetadataName() {
	spec := validWorkspaceSpec()
	spec.Metadata.Name = ""
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata.name")
}

func (s *SpecSuite) TestValidateMissingSourceType() {
	spec := validWorkspaceSpec()
	spec.Source.Type = ""
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "source.type")
}

func (s *SpecSuite) TestValidateInvalidSourceType() {
	spec := validWorkspaceSpec()
	spec.Source.Type = workspace.SourceType("invalid")
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "source.type")
}

func (s *SpecSuite) TestValidateGitSourceMissingURL() {
	spec := validWorkspaceSpec()
	spec.Source = workspace.Source{
		Type: workspace.SourceTypeGit,
		Git:  workspace.GitSource{URL: ""},
	}
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "source.url")
}

func (s *SpecSuite) TestValidateLocalSourceMissingPath() {
	spec := validWorkspaceSpec()
	spec.Source = workspace.Source{
		Type:  workspace.SourceTypeLocal,
		Local: workspace.LocalSource{Path: ""},
	}
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "source.path")
}

func (s *SpecSuite) TestValidateLocalSourceRelativePath() {
	spec := validWorkspaceSpec()
	spec.Source = workspace.Source{
		Type:  workspace.SourceTypeLocal,
		Local: workspace.LocalSource{Path: "relative/path"},
	}
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "absolute path")
}

func (s *SpecSuite) TestValidateLocalSourceAbsolutePath() {
	spec := validWorkspaceSpec()
	spec.Source = workspace.Source{
		Type:  workspace.SourceTypeLocal,
		Local: workspace.LocalSource{Path: "/absolute/path"},
	}
	s.NoError(workspace.ValidateWorkspaceSpec(spec))
}

func (s *SpecSuite) TestValidateEmptyDirSource() {
	spec := validWorkspaceSpec()
	spec.Source = workspace.Source{
		Type: workspace.SourceTypeEmptyDir,
	}
	s.NoError(workspace.ValidateWorkspaceSpec(spec))
}

func (s *SpecSuite) TestValidateHooksSetupMissingCommand() {
	spec := validWorkspaceSpec()
	spec.Hooks = workspace.Hooks{
		Setup: []workspace.Hook{
			{Command: ""},
		},
	}
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "setup")
	s.Contains(err.Error(), "command")
}

func (s *SpecSuite) TestValidateHooksTeardownMissingCommand() {
	spec := validWorkspaceSpec()
	spec.Hooks = workspace.Hooks{
		Teardown: []workspace.Hook{
			{Command: ""},
		},
	}
	err := workspace.ValidateWorkspaceSpec(spec)
	s.Require().Error(err)
	s.Contains(err.Error(), "teardown")
	s.Contains(err.Error(), "command")
}

func (s *SpecSuite) TestValidateValidHooks() {
	spec := validWorkspaceSpec()
	spec.Hooks = workspace.Hooks{
		Setup: []workspace.Hook{
			{Command: "npm", Args: []string{"install"}},
		},
		Teardown: []workspace.Hook{
			{Command: "docker", Args: []string{"compose", "down"}},
		},
	}
	s.NoError(workspace.ValidateWorkspaceSpec(spec))
}

func (s *SpecSuite) TestSourceTypeIsValid() {
	s.True(workspace.SourceTypeGit.IsValid())
	s.True(workspace.SourceTypeEmptyDir.IsValid())
	s.True(workspace.SourceTypeLocal.IsValid())
	s.False(workspace.SourceType("invalid").IsValid())
	s.False(workspace.SourceType("").IsValid())
}

func (s *SpecSuite) TestSourceTypeString() {
	s.Equal("git", workspace.SourceTypeGit.String())
	s.Equal("emptyDir", workspace.SourceTypeEmptyDir.String())
	s.Equal("local", workspace.SourceTypeLocal.String())
}

func (s *SpecSuite) TestParseCompleteExample() {
	// Test the full Go project example from the design doc.
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {
			"name": "backend-service",
			"annotations": {
				"org.openagents.workspace.language": "go"
			}
		},
		"source": {
			"type": "git",
			"url": "https://github.com/org/backend.git",
			"ref": "main"
		},
		"hooks": {
			"setup": [
				{
					"command": "go",
					"args": ["mod", "download"],
					"description": "下载 Go 模块"
				}
			]
		}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal("backend-service", spec.Metadata.Name)
	s.Equal("go", spec.Metadata.Annotations["org.openagents.workspace.language"])
	s.Equal(workspace.SourceTypeGit, spec.Source.Type)
	s.Equal("https://github.com/org/backend.git", spec.Source.Git.URL)
	s.Equal("main", spec.Source.Git.Ref)
	s.Len(spec.Hooks.Setup, 1)
	s.Equal("go", spec.Hooks.Setup[0].Command)
	s.Equal([]string{"mod", "download"}, spec.Hooks.Setup[0].Args)
	s.NoError(workspace.ValidateWorkspaceSpec(spec))
}

func (s *SpecSuite) TestParseNoHooks() {
	// Test Git project without hooks.
	data := []byte(`{
		"massVersion": "0.1.0",
		"metadata": {"name": "simple-project"},
		"source": {"type": "git", "url": "https://github.com/org/simple.git"}
	}`)
	spec, err := workspace.ParseWorkspaceSpec(data)
	s.Require().NoError(err)
	s.Equal("simple-project", spec.Metadata.Name)
	s.Equal(workspace.SourceTypeGit, spec.Source.Type)
	s.NoError(workspace.ValidateWorkspaceSpec(spec))
}

func TestSpecSuite(t *testing.T) {
	suite.Run(t, new(SpecSuite))
}
