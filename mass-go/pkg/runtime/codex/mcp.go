package codex

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	sdk "github.com/zoumo/agent-sdk-go/codex"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func codexMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) ([]sdk.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, err
	}
	if err := validateCodexMCPIsolation(options); err != nil {
		return nil, err
	}
	converted := make([]sdk.MCPServer, 0, len(servers))
	for i, server := range servers {
		if strings.Contains(server.Name, ".") {
			return nil, fmt.Errorf("mcp server %d: Codex names must not contain dots", i)
		}
		switch server.Type {
		case "stdio":
			converted = append(converted, sdk.MCPServer{
				Name:    server.Name,
				Command: server.Command,
				Args:    append([]string(nil), server.Args...),
				Env:     maps.Clone(server.Env),
				Cwd:     server.Cwd,
			})
		case "http":
			converted = append(converted, sdk.MCPServer{
				Name:              server.Name,
				URL:               server.URL,
				Headers:           maps.Clone(server.Headers),
				EnvHeaders:        maps.Clone(server.EnvHeaders),
				BearerTokenEnvVar: server.BearerTokenEnvVar,
			})
		case "sse":
			return nil, fmt.Errorf("mcp server %d: Codex does not support sse transport", i)
		}
	}
	return converted, nil
}

func validateCodexMCPIsolation(options runtime.ProcessConfig) error {
	home, explicit := options.Env["CODEX_HOME"]
	if !explicit || !filepath.IsAbs(home) {
		return errors.New("codex mcpServers require explicit absolute env.CODEX_HOME")
	}
	for _, ambient := range codexAmbientHomes(options) {
		if sameCodexHome(home, ambient) {
			return errors.New("codex mcpServers require an isolated CODEX_HOME")
		}
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return errors.New("codex mcpServers require an existing CODEX_HOME directory")
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("codex mcpServers cannot read CODEX_HOME config.toml")
	}
	var config map[string]any
	if err := toml.Unmarshal(b, &config); err != nil {
		return errors.New("codex mcpServers require valid CODEX_HOME config.toml")
	}
	if containsMCPConfig(config) {
		return errors.New("codex mcpServers require CODEX_HOME without ambient mcp_servers")
	}
	return nil
}

func codexAmbientHomes(options runtime.ProcessConfig) []string {
	homes := []string{}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		homes = append(homes, home)
	}
	if home := os.Getenv("HOME"); home != "" {
		homes = append(homes, filepath.Join(home, ".codex"))
	}
	childHome := options.Env["HOME"]
	if childHome == "" && options.EnvPolicy != "replace" {
		childHome = os.Getenv("HOME")
	}
	if childHome != "" {
		homes = append(homes, filepath.Join(childHome, ".codex"))
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(userHome, ".codex"))
	}
	return homes
}

func sameCodexHome(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func containsMCPConfig(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "mcp_servers" || containsMCPConfig(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(typed, containsMCPConfig)
	}
	return false
}
