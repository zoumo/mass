package spec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	apispec "github.com/zoumo/oar/api/spec"
	"github.com/zoumo/oar/pkg/spec"
)

// validConfig returns a Config that passes all validation rules.
func validConfig() apispec.Config {
	return apispec.Config{
		OarVersion: "0.1.0",
		Metadata: apispec.Metadata{
			Name: "test-agent",
		},
		AgentRoot: apispec.AgentRoot{Path: "workspace"},
		AcpAgent: apispec.AcpAgent{
			Process: apispec.AcpProcess{
				Command: "/usr/bin/agent",
			},
		},
		Permissions: apispec.ApproveAll,
	}
}

// writeConfigFile writes c as config.json into dir and returns the dir path.
func writeConfigFile(t *testing.T, dir string, c apispec.Config) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// ---- suite ----

type ConfigSuite struct {
	suite.Suite
	dir string
}

func (s *ConfigSuite) SetupTest() {
	var err error
	s.dir, err = os.MkdirTemp("", "oar-config-test-*")
	s.Require().NoError(err)
}

func (s *ConfigSuite) TeardownTest() {
	_ = os.RemoveAll(s.dir)
}

func (s *ConfigSuite) TestParseValid() {
	writeConfigFile(s.T(), s.dir, validConfig())
	c, err := spec.ParseConfig(s.dir)
	s.Require().NoError(err)
	s.Equal("test-agent", c.Metadata.Name)
	s.Equal("/usr/bin/agent", c.AcpAgent.Process.Command)
}

func (s *ConfigSuite) TestParseMissingFile() {
	_, err := spec.ParseConfig(s.dir) // config.json not written
	s.Require().Error(err)
	s.Contains(err.Error(), "config.json")
}

func (s *ConfigSuite) TestParseMalformedJSON() {
	err := os.WriteFile(filepath.Join(s.dir, "config.json"), []byte("{not json}"), 0o600)
	s.Require().NoError(err)
	_, err = spec.ParseConfig(s.dir)
	s.Require().Error(err)
}

func (s *ConfigSuite) TestValidateValid() {
	s.NoError(spec.ValidateConfig(validConfig()))
}

func (s *ConfigSuite) TestValidateMissingOarVersion() {
	c := validConfig()
	c.OarVersion = ""
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "oarVersion")
}

func (s *ConfigSuite) TestValidateUnknownMajorVersion() {
	c := validConfig()
	c.OarVersion = "1.0.0" // major == 1, unsupported
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "major")
}

func (s *ConfigSuite) TestValidateMissingMetadataName() {
	c := validConfig()
	c.Metadata.Name = ""
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata.name")
}

func (s *ConfigSuite) TestValidateMissingCommand() {
	c := validConfig()
	c.AcpAgent.Process.Command = ""
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "command")
}

func (s *ConfigSuite) TestValidateMissingAgentRootPath() {
	c := validConfig()
	c.AgentRoot.Path = ""
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "agentRoot.path")
}

func (s *ConfigSuite) TestValidateAbsoluteAgentRootPath() {
	c := validConfig()
	c.AgentRoot.Path = "/absolute/path"
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "relative")
}

func (s *ConfigSuite) TestResolveAgentRoot_PlainDir() {
	subdir := filepath.Join(s.dir, "workspace")
	s.Require().NoError(os.MkdirAll(subdir, 0o755))

	c := validConfig()
	c.AgentRoot.Path = "workspace"

	resolved, err := spec.ResolveAgentRoot(s.dir, c)
	s.Require().NoError(err)
	wantDir, err := filepath.EvalSymlinks(subdir)
	s.Require().NoError(err)
	s.Equal(wantDir, resolved)
}

func (s *ConfigSuite) TestResolveAgentRoot_Symlink() {
	target := filepath.Join(s.dir, "actual-workspace")
	s.Require().NoError(os.MkdirAll(target, 0o755))

	link := filepath.Join(s.dir, "workspace")
	s.Require().NoError(os.Symlink(target, link))

	c := validConfig()
	c.AgentRoot.Path = "workspace"

	resolved, err := spec.ResolveAgentRoot(s.dir, c)
	s.Require().NoError(err)
	wantTarget, err := filepath.EvalSymlinks(target)
	s.Require().NoError(err)
	s.Equal(wantTarget, resolved)
}

func (s *ConfigSuite) TestResolveAgentRoot_NonExistent() {
	c := validConfig()
	c.AgentRoot.Path = "nonexistent"

	_, err := spec.ResolveAgentRoot(s.dir, c)
	s.Require().Error(err)
	s.Contains(err.Error(), "agentRoot")
}

func (s *ConfigSuite) TestValidateInvalidPermissions() {
	c := validConfig()
	c.Permissions = apispec.PermissionPolicy("bad-policy")
	err := spec.ValidateConfig(c)
	s.Require().Error(err)
	s.Contains(err.Error(), "permissions")
}

func (s *ConfigSuite) TestValidateEmptyPermissionsDefault() {
	c := validConfig()
	c.Permissions = "" // omitted — should default to valid
	s.NoError(spec.ValidateConfig(c))
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}
