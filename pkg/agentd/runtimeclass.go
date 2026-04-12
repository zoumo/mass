// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// The RuntimeClass type has been removed — agent launch configuration is now
// read directly from meta.Agent (the agent definition record).
// See process.go for the generateConfig function that uses *meta.Agent.
package agentd
