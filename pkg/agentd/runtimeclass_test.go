// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// The RuntimeClass type and NewRuntimeClassFromMeta constructor have been removed.
// Agent launch configuration is now read directly from meta.Agent.
// This file is intentionally empty; all tests for launch config generation
// are in process_test.go (TestGenerateConfig).
package agentd
