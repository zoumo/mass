// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file tests the RuntimeClass registry functionality.
package agentd

import (
	"os"
	"testing"
)

func TestNewRuntimeClassRegistryValidConfig(t *testing.T) {
	configs := map[string]RuntimeClassConfig{
		"python": {
			Command: "python",
			Args:    []string{"-m", "agent_runtime"},
			Env:     map[string]string{"PYTHONPATH": "/app"},
			Capabilities: CapabilitiesConfig{
				Streaming:          true,
				SessionLoad:        false,
				ConcurrentSessions: 5,
			},
		},
		"nodejs": {
			Command: "node",
			Args:    []string{"runtime.js"},
			Env:     nil,
			Capabilities: CapabilitiesConfig{
				Streaming:          false,
				SessionLoad:        true,
				ConcurrentSessions: 10,
			},
		},
		"bash": {
			Command: "bash",
			Args:    nil,
			Env:     map[string]string{"PATH": "/usr/bin"},
		},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry returned unexpected error: %v", err)
	}

	if registry == nil {
		t.Fatal("NewRuntimeClassRegistry returned nil registry")
	}

	// Verify all classes loaded.
	if len(registry.classes) != 3 {
		t.Errorf("expected 3 classes, got %d", len(registry.classes))
	}
}

func TestGetFoundAndNotFound(t *testing.T) {
	configs := map[string]RuntimeClassConfig{
		"python": {
			Command: "python",
		},
		"nodejs": {
			Command: "node",
		},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry returned unexpected error: %v", err)
	}

	// Test Get found.
	class, err := registry.Get("python")
	if err != nil {
		t.Errorf("Get(python) returned unexpected error: %v", err)
	}
	if class == nil {
		t.Error("Get(python) returned nil class")
	}
	if class.Name != "python" {
		t.Errorf("expected Name=python, got %s", class.Name)
	}
	if class.Command != "python" {
		t.Errorf("expected Command=python, got %s", class.Command)
	}

	// Test Get not found.
	class, err = registry.Get("nonexistent")
	if err == nil {
		t.Error("Get(nonexistent) should return error")
	}
	if class != nil {
		t.Error("Get(nonexistent) should return nil class")
	}
	expectedErrMsg := "runtime class not found: nonexistent"
	if err.Error() != expectedErrMsg {
		t.Errorf("expected error message '%s', got '%s'", expectedErrMsg, err.Error())
	}
}

func TestEnvSubstitution(t *testing.T) {
	// Set environment variable for substitution test.
	os.Setenv("TEST_VAR", "resolved_value")
	defer os.Unsetenv("TEST_VAR")

	configs := map[string]RuntimeClassConfig{
		"test": {
			Command: "test-command",
			Env:     map[string]string{"RESOLVED": "${TEST_VAR}", "STATIC": "static_value"},
		},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry returned unexpected error: %v", err)
	}

	class, err := registry.Get("test")
	if err != nil {
		t.Fatalf("Get(test) returned unexpected error: %v", err)
	}

	// Verify ${VAR} was resolved.
	if class.Env["RESOLVED"] != "resolved_value" {
		t.Errorf("expected RESOLVED=resolved_value, got %s", class.Env["RESOLVED"])
	}

	// Verify static value unchanged.
	if class.Env["STATIC"] != "static_value" {
		t.Errorf("expected STATIC=static_value, got %s", class.Env["STATIC"])
	}
}

func TestCommandRequired(t *testing.T) {
	configs := map[string]RuntimeClassConfig{
		"missing-command": {
			Command: "", // Empty command should fail validation.
		},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err == nil {
		t.Error("NewRuntimeClassRegistry should return error for missing Command")
	}
	if registry != nil {
		t.Error("NewRuntimeClassRegistry should return nil registry on error")
	}
	expectedErrMsg := "runtime class missing-command: command is required"
	if err.Error() != expectedErrMsg {
		t.Errorf("expected error message '%s', got '%s'", expectedErrMsg, err.Error())
	}
}

func TestCapabilitiesDefaults(t *testing.T) {
	// Test with unspecified capabilities (zero values from YAML).
	configs := map[string]RuntimeClassConfig{
		"default-caps": {
			Command: "test",
			// Capabilities not specified - should get defaults.
		},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry returned unexpected error: %v", err)
	}

	class, err := registry.Get("default-caps")
	if err != nil {
		t.Fatalf("Get(default-caps) returned unexpected error: %v", err)
	}

	// Verify Streaming default is true.
	if !class.Capabilities.Streaming {
		t.Error("expected Streaming default=true, got false")
	}

	// Verify SessionLoad default is false.
	if class.Capabilities.SessionLoad {
		t.Error("expected SessionLoad default=false, got true")
	}

	// Verify ConcurrentSessions default is 1.
	if class.Capabilities.ConcurrentSessions != 1 {
		t.Errorf("expected ConcurrentSessions default=1, got %d", class.Capabilities.ConcurrentSessions)
	}
}

func TestList(t *testing.T) {
	configs := map[string]RuntimeClassConfig{
		"python": {Command: "python"},
		"nodejs": {Command: "node"},
		"bash":   {Command: "bash"},
	}

	registry, err := NewRuntimeClassRegistry(configs)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry returned unexpected error: %v", err)
	}

	list := registry.List()
	if len(list) != 3 {
		t.Errorf("expected List to return 3 classes, got %d", len(list))
	}

	// Verify each class is present (order not guaranteed for map iteration).
	names := make(map[string]bool)
	for _, class := range list {
		names[class.Name] = true
	}
	for expected := range configs {
		if !names[expected] {
			t.Errorf("expected class %s in List results", expected)
		}
	}
}
