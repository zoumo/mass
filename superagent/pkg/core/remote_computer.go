package core

import (
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"syscall"

	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

type RemoteComputerConfig struct {
	CUA CUAConfig `mapstructure:"cua"`
}

type CUAConfig struct {
	Command    string `mapstructure:"command"`
	ConfigFile string `mapstructure:"configFile"`
}

func decodeRemoteComputer(fields map[string]any) (*RemoteComputerConfig, error) {
	var section map[string]any
	for name, value := range fields {
		if !strings.EqualFold(name, "remoteComputer") {
			continue
		}
		if section != nil {
			return nil, problem("invalid_argument", "configuration contains duplicate remoteComputer fields")
		}
		var ok bool
		section, ok = value.(map[string]any)
		if !ok || len(section) != 1 {
			return nil, problem("invalid_argument", "remoteComputer requires a cua mapping")
		}
	}
	if section == nil {
		return nil, nil
	}

	var cua map[string]any
	for provider, value := range section {
		if strings.EqualFold(provider, "cua") {
			cua, _ = value.(map[string]any)
		}
	}
	if len(cua) != 2 {
		return nil, problem("invalid_argument", "remoteComputer.cua requires command and configFile")
	}
	var result RemoteComputerConfig
	for field, value := range cua {
		path, ok := value.(string)
		if !ok || strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
			return nil, problem("invalid_argument", "remoteComputer.cua paths must be nonempty strings")
		}
		switch strings.ToLower(field) {
		case "command":
			result.CUA.Command = path
		case "configfile":
			result.CUA.ConfigFile = path
		default:
			return nil, problem("invalid_argument", "remoteComputer.cua requires command and configFile")
		}
	}
	if result.CUA.Command == "" || result.CUA.ConfigFile == "" {
		return nil, problem("invalid_argument", "remoteComputer.cua requires command and configFile")
	}
	return &result, nil
}

func (c *RemoteComputerConfig) resolvePaths(resolve func(string) string) error {
	c.CUA.Command = resolve(c.CUA.Command)
	c.CUA.ConfigFile = resolve(c.CUA.ConfigFile)
	if _, err := exec.LookPath(c.CUA.Command); err != nil {
		return problem("invalid_argument", "remoteComputer.cua.command must be executable")
	}
	file, err := os.OpenFile(c.CUA.ConfigFile, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return problem("invalid_argument", "remoteComputer.cua.configFile must be a readable regular file")
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return problem("invalid_argument", "remoteComputer.cua.configFile must be a readable regular file")
	}
	return nil
}

const cuaMCPName = "cua-mcp"

func (c *RemoteComputerConfig) cuaMCP(role string) *runtime.MCPServer {
	if c == nil || role != "topic" && role != "issue" {
		return nil
	}
	return &runtime.MCPServer{
		Name: cuaMCPName, Type: runtime.MCPTransportStdio, Command: c.CUA.Command,
		Args: []string{"--config", c.CUA.ConfigFile},
	}
}

func (c *RemoteComputerConfig) applySessionTools(role string, spec *resource.SessionSpec) {
	if spec.Features != nil || c != nil {
		features := runtime.CloneSessionFeatures(spec.Features)
		if features == nil {
			features = &runtime.SessionFeatures{}
		}
		features.RestrictLocalTools = nil
		if c != nil {
			features.RestrictLocalTools = new(true)
		}
		spec.Features = features
	}
	cua := c.cuaMCP(role)
	if spec.MCPServers != nil || cua != nil {
		var servers []runtime.MCPServer
		if spec.MCPServers != nil {
			servers = slices.DeleteFunc(slices.Clone(*spec.MCPServers), func(server runtime.MCPServer) bool {
				return server.Name == cuaMCPName
			})
		}
		if cua != nil {
			servers = append(servers, *cua)
		}
		spec.MCPServers = &servers
	}
}

func (c *RemoteComputerConfig) matchesSessionTools(role string, spec resource.SessionSpec) bool {
	restricted := spec.Features != nil && spec.Features.RestrictLocalToolsEnabled()
	if restricted != (c != nil) {
		return false
	}
	var actual *runtime.MCPServer
	if spec.MCPServers != nil {
		for _, server := range *spec.MCPServers {
			if server.Name == cuaMCPName {
				if actual != nil {
					return false
				}
				actual = &server
			}
		}
	}
	return reflect.DeepEqual(actual, c.cuaMCP(role))
}

const remoteComputerConfigurationChanged = "remote_computer_configuration_changed"
