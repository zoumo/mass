package core_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestRoleSessionConfigurationDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "core.yaml")
	defaultTemplate := core.IssueTemplate{Name: "default", ExecutionProfile: core.ExecutionProfile{RuntimeRef: "codex"}}
	for _, tc := range []struct {
		name, yaml, namespace, workspace string
		router, topic                    core.ExecutionProfile
		template                         core.IssueTemplate
	}{
		{"omitted", "", "superagent", "state/router-workspace", core.ExecutionProfile{RuntimeRef: "claude"}, core.ExecutionProfile{RuntimeRef: "codex"}, defaultTemplate},
		{"empty", "sessions: {}\n", "superagent", "state/router-workspace", core.ExecutionProfile{RuntimeRef: "claude"}, core.ExecutionProfile{RuntimeRef: "codex"}, defaultTemplate},
		{"empty roles", "sessions: {router: {}, topic: {}, issue: {}}\n", "superagent", "state/router-workspace", core.ExecutionProfile{RuntimeRef: "claude"}, core.ExecutionProfile{RuntimeRef: "codex"}, defaultTemplate},
		{
			name:      "partial override",
			yaml:      "sessions: {namespace: custom, router: {model: router-model, workspace: ./other/router}, topic: {effort: high}}\n",
			namespace: "custom", workspace: "other/router",
			router: core.ExecutionProfile{RuntimeRef: "claude", Model: "router-model"}, topic: core.ExecutionProfile{RuntimeRef: "codex", Effort: "high"},
			template: defaultTemplate,
		},
		{
			name:      "explicit templates",
			yaml:      "sessions: {issue: {templates: [{name: custom, runtimeRef: agent, model: issue-model, effort: medium}]}}\n",
			namespace: "superagent", workspace: "state/router-workspace",
			router: core.ExecutionProfile{RuntimeRef: "claude"}, topic: core.ExecutionProfile{RuntimeRef: "codex"},
			template: core.IssueTemplate{Name: "custom", ExecutionProfile: core.ExecutionProfile{RuntimeRef: "agent", Model: "issue-model", Effort: "medium"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "dataDir: ./state\n" + tc.yaml
			write(t, configPath, raw)
			config, err := core.LoadConfig(configPath)
			require.NoError(t, err)
			composed, err := core.LoadComposeConfig(configPath, strings.NewReader(raw))
			require.NoError(t, err)
			require.Equal(t, config.Sessions, composed.Sessions)
			require.Equal(t, tc.namespace, config.Sessions.Namespace)
			require.Equal(t, tc.router, config.Sessions.Router.ExecutionProfile)
			require.Equal(t, filepath.Join(dir, tc.workspace), config.Sessions.Router.Workspace)
			require.Equal(t, tc.topic, config.Sessions.Topic)
			require.Equal(t, []core.IssueTemplate{tc.template}, config.Sessions.Issue.Templates)
			require.NoDirExists(t, config.Sessions.Router.Workspace, "loading configuration must not create the workspace")
		})
	}
	for _, tc := range []struct{ yaml, field string }{
		{"sessions: null\n", "sessions"},
		{"sessions: {router: null}\n", "sessions"},
		{"sessions: {topic: []}\n", "sessions.topic"},
		{"sessions: {issue: null}\n", "sessions"},
		{"sessions: {issue: {templates: [null]}}\n", "sessions.issue.templates"},
		{"listen: 127.0.0.1:0\n", "fixed listen port"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			write(t, configPath, tc.yaml)
			_, err := core.LoadConfig(configPath)
			require.ErrorContains(t, err, tc.field)
			_, err = core.LoadComposeConfig(configPath, strings.NewReader(tc.yaml))
			require.ErrorContains(t, err, tc.field)
		})
	}
}

func TestRoleSessionDefaultsThroughCLI(t *testing.T) {
	for _, entry := range []string{"serve", "compose"} {
		t.Run(entry, func(t *testing.T) {
			boundary := newRouteBoundaryWithRuntime(t, "fixture", "claude", "codex")
			boundary.driver.allowResume = true
			massd, err := client.New(boundary.url, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, massd.Close()) })

			f := fixtureAt(t)
			f.seedToken("triggers.token", f.token)
			raw := fmt.Sprintf("dataDir: ./data\nlisten: 127.0.0.1:0\n%smassd:\n  endpoint: %q\n", fixtureStableMCP, boundary.url)
			if entry == "compose" {
				raw = "core:\n  " + strings.ReplaceAll(strings.TrimSuffix(raw, "\n"), "\n", "\n  ") + "\n"
			}
			write(t, f.config, raw)
			p := f.startCommand(entry, f.config)
			workspace := filepath.Join(f.data, "router-workspace")
			require.DirExists(t, workspace)
			routerBinding := waitBinding(t, p, "router", "", "ready")
			require.Equal(t, "superagent-router", routerBinding.Namespace)
			assertDefaultSession(t, massd, routerBinding, "claude", workspace)

			message := submit(t, p, f, input("defaults"))
			waitDelivered(t, p, message.MessageID, "router", "")
			router := startRoleMCPAt(t, f, routerBinding, p.endpoint)
			route := newRoute("defaults", []string{message.MessageID}, f.dir)
			decision := mcpWork[core.RouteDecision](t, router, "route_messages", route)
			topicID := decision.Targets[0].TopicID
			topicBinding := waitBinding(t, p, "topic", topicID, "ready")
			assertDefaultSession(t, massd, topicBinding, "codex", f.dir)
			topic := startRoleMCPAt(t, f, topicBinding, p.endpoint)
			require.Equal(t, []map[string]string{{"name": "default", "description": ""}}, queryTemplates(t, topic))
			issueRequest := issueInput(f.dir, "default-issue")
			issueRequest.Start = true
			work := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueRequest)
			require.NotNil(t, work.SessionBinding)
			issueBinding := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
			assertDefaultSession(t, massd, issueBinding, "codex", f.dir)
			p.stop(t, false)

			p = f.startCommand(entry, f.config)
			restarted := waitBinding(t, p, "router", "", "ready")
			require.Equal(t, routerBinding.ID, restarted.ID)
			require.Equal(t, routerBinding.SessionUID, restarted.SessionUID)
			require.DirExists(t, workspace)
		})
	}
}

func assertDefaultSession(t *testing.T, massd *client.Client, binding core.SessionBinding, runtimeName, workspace string) {
	t.Helper()
	var session struct {
		Spec struct {
			RuntimeRef string          `json:"runtimeRef"`
			Cwd        string          `json:"cwd"`
			Config     json.RawMessage `json:"config"`
		} `json:"spec"`
	}
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
	require.Equal(t, runtimeName, session.Spec.RuntimeRef)
	require.Equal(t, workspace, session.Spec.Cwd)
	require.JSONEq(t, `{"permissionPreset":"fullAccess"}`, string(session.Spec.Config))
}

func TestRouterWorkspacePreparation(t *testing.T) {
	f := fixtureAt(t)
	base := "dataDir: ./data\nlisten: 127.0.0.1:0\n" + fixtureStableMCP + "sessions: {router: {workspace: ./data/workspaces/router}}\n"
	write(t, f.config, base)
	locked := filepath.Join(f.data, "workspaces")
	require.NoError(t, os.MkdirAll(f.data, 0o700))
	write(t, locked, "not a directory")
	f.fails("sessions.router.workspace")
	require.NoFileExists(t, filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, os.Remove(locked))

	p := f.start()
	workspace := filepath.Join(locked, "router")
	require.DirExists(t, workspace)
	p.stop(t, false)
	p = f.start()
	require.DirExists(t, workspace)
	p.stop(t, false)

	external := fixtureAt(t)
	write(t, external.config, "dataDir: ./data\nlisten: 127.0.0.1:0\n"+fixtureStableMCP+"sessions: {router: {workspace: ./outside/router}}\n")
	write(t, filepath.Join(external.dir, "outside"), "not a directory")
	external.fails("cannot create sessions.router.workspace directory")
	require.NoError(t, os.Remove(filepath.Join(external.dir, "outside")))
	external.start()
	require.DirExists(t, filepath.Join(external.dir, "outside", "router"))
}
