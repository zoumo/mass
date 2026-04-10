// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file tests NewRuntimeClassFromMeta.
package agentd

import (
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

func TestNewRuntimeClassFromMeta_BasicFields(t *testing.T) {
	rt := &meta.Runtime{
		Metadata: meta.ObjectMeta{Name: "mockagent"},
		Spec: meta.RuntimeSpec{
			Command: "/usr/bin/mockagent",
			Args:    []string{"--mode", "test"},
		},
	}
	rc := NewRuntimeClassFromMeta(rt)

	if rc.Name != "mockagent" {
		t.Errorf("expected Name=mockagent, got %q", rc.Name)
	}
	if rc.Command != "/usr/bin/mockagent" {
		t.Errorf("expected Command=/usr/bin/mockagent, got %q", rc.Command)
	}
	if len(rc.Args) != 2 || rc.Args[0] != "--mode" || rc.Args[1] != "test" {
		t.Errorf("unexpected Args: %v", rc.Args)
	}
}

func TestNewRuntimeClassFromMeta_EmptyEnv(t *testing.T) {
	rt := &meta.Runtime{
		Metadata: meta.ObjectMeta{Name: "bare"},
		Spec: meta.RuntimeSpec{
			Command: "bare-cmd",
		},
	}
	rc := NewRuntimeClassFromMeta(rt)

	if rc.Env != nil {
		t.Errorf("expected nil Env for zero-Env spec, got %v", rc.Env)
	}
}

func TestNewRuntimeClassFromMeta_EnvSlice(t *testing.T) {
	rt := &meta.Runtime{
		Metadata: meta.ObjectMeta{Name: "envtest"},
		Spec: meta.RuntimeSpec{
			Command: "envtest-cmd",
			Env: []spec.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAUD", Value: "9600"},
			},
		},
	}
	rc := NewRuntimeClassFromMeta(rt)

	if len(rc.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(rc.Env))
	}
	if rc.Env[0].Name != "FOO" || rc.Env[0].Value != "bar" {
		t.Errorf("unexpected Env[0]: %+v", rc.Env[0])
	}
	if rc.Env[1].Name != "BAUD" || rc.Env[1].Value != "9600" {
		t.Errorf("unexpected Env[1]: %+v", rc.Env[1])
	}
}
