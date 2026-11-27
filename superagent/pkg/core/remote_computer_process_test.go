package core_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

const remoteComputerFixture = "remoteComputer:\n  cua:\n    command: ./cua-mcp\n    configFile: ./cua.yaml\n"

func remoteComputerFixtureFiles(t *testing.T, f *fixture) {
	t.Helper()
	// The controlled native app-server records Session configuration without
	// starting subprocess MCP servers from that configuration.
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "cua-mcp"), []byte("#!/bin/sh\nexit 1\n"), 0o700))
	write(t, filepath.Join(f.dir, "cua.yaml"), "{}\n")
}

func TestRemoteComputerConfig(t *testing.T) {
	f := fixtureAt(t)
	remoteComputerFixtureFiles(t, f)
	base, err := os.ReadFile(f.config)
	require.NoError(t, err)
	loaders := map[string]func(string) (core.Config, error){
		"standalone": func(addition string) (core.Config, error) {
			write(t, f.config, string(base)+addition)
			return core.LoadConfig(f.config)
		},
		"compose": func(addition string) (core.Config, error) {
			return core.LoadComposeConfig(f.config, strings.NewReader(string(base)+addition))
		},
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			c, err := load("")
			require.NoError(t, err)
			require.Nil(t, c.RemoteComputer)
			c, err = load(remoteComputerFixture)
			require.NoError(t, err)
			require.Equal(t, &core.RemoteComputerConfig{CUA: core.CUAConfig{
				Command: filepath.Join(f.dir, "cua-mcp"), ConfigFile: filepath.Join(f.dir, "cua.yaml"),
			}}, c.RemoteComputer)
			for _, invalid := range []string{
				"remoteComputer: null\n",
				"remoteComputer: {}\n",
				"remoteComputer: {other: {}}\n",
				"remoteComputer: {cua: null}\n",
				"remoteComputer: {cua: {command: ./cua-mcp}}\n",
				"remoteComputer: {cua: {command: 7, configFile: ./cua.yaml}}\n",
				"remoteComputer: {cua: {command: ./cua-mcp, ConfigFile: ./cua.yaml, configfile: ./cua.yaml}}\n",
				"remoteComputer: {cua: {command: ./cua-mcp, Command: ./cua.yaml}}\n",
				remoteComputerFixture + strings.ReplaceAll(remoteComputerFixture, "remoteComputer", "RemoteComputer"),
				strings.Replace(remoteComputerFixture, "command: ./cua-mcp", "extra: ./cua-mcp", 1),
				strings.Replace(remoteComputerFixture, "command: ./cua-mcp", "command: ./cua.yaml", 1),
				strings.Replace(remoteComputerFixture, "configFile: ./cua.yaml", "configFile: .", 1),
				strings.Replace(remoteComputerFixture, "configFile: ./cua.yaml", "configFile: ./missing.yaml", 1),
			} {
				_, err := load(invalid)
				var configErr *core.Error
				require.ErrorAs(t, err, &configErr, "input %q", invalid)
				require.Equal(t, "invalid_argument", configErr.Code)
			}
		})
	}
}

func readRemoteComputerSession(t *testing.T, massd *client.Client, binding core.SessionBinding, dir, config string) resource.Session {
	t.Helper()
	var session resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
	require.Equal(t, binding.SessionUID, session.Metadata.UID)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false), RestrictLocalTools: new(true)}, session.Spec.Features)
	require.Equal(t, session.Spec.Features, session.Status.EffectiveFeatures)
	require.NotNil(t, session.Spec.MCPServers)
	servers := *session.Spec.MCPServers
	require.NotEmpty(t, servers)
	require.Equal(t, "superagent", servers[0].Name)
	require.Equal(t, runtime.MCPTransportHTTP, servers[0].Type)
	if binding.Role == "router" {
		require.Len(t, servers, 1)
		return session
	}
	require.Len(t, servers, 2)
	require.Equal(t, runtime.MCPServer{
		Name: "cua-mcp", Type: runtime.MCPTransportStdio, Command: filepath.Join(dir, "cua-mcp"),
		Args: []string{"--config", filepath.Join(dir, config)},
	}, servers[1])
	return session
}

func TestRemoteComputerRoleSessionsAndReplacement(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, operator := eventFixture(t, endpoint)
	remoteComputerFixtureFiles(t, f)
	write(t, filepath.Join(f.dir, "changed-cua.yaml"), "{}\n")
	file, err := os.ReadFile(f.config)
	require.NoError(t, err)

	// Initialize the Router binding with the old config. Core has never sent a
	// create for it, so the first process can adopt the new config in place.
	c, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	store, err := core.OpenStore(c, core.TelemetryOptions{})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	config := string(file) + remoteComputerFixture
	write(t, f.config, config)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, 1, routerBinding.Generation)
	readRemoteComputerSession(t, massd, routerBinding, f.dir, "")
	router := startRoleMCP(t, f, routerBinding)
	topicBinding, _ := nativeWorkTopic(t, f, p, peer, router, "remote-computer")
	originalTopicSession := readRemoteComputerSession(t, massd, topicBinding, f.dir, "cua.yaml")
	topic := startRoleMCP(t, f, topicBinding)
	require.Len(t, queryTemplates(t, topic), 2)

	for _, model := range []core.ExecutionProfile{issueModel, reviewModel} {
		request := issueInput(f.dir, "make-remote-"+model.Model)
		request.RuntimeRef, request.Model, request.Start = model.RuntimeRef, model.Model, true
		work := mcpWork[core.WorkReceipt](t, topic, "create_issue", request)
		require.NotNil(t, work.SessionBinding)
		binding := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
		readRemoteComputerSession(t, massd, binding, f.dir, "cua.yaml")
		call := peer.next(t, "turn/start")
		require.Equal(t, binding.SessionID, call.session)
		call.accept()
		waitWorkEvent(t, p, binding.StartEventID, "sent")
	}

	var direct resource.Session
	require.NoError(t, massd.CreateSession(t.Context(), resource.Session{
		Metadata: resource.Metadata{Namespace: "direct", Name: "remote-computer"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: f.dir}},
	}, &direct))
	require.Nil(t, direct.Spec.Features)
	require.Nil(t, direct.Spec.MCPServers)
	require.False(t, direct.Status.EffectiveFeatures.RestrictLocalToolsEnabled())

	p.stop(t, false)
	p = f.start()
	restarted := waitBinding(t, p, "topic", topicBinding.OwnerID, "ready")
	require.Equal(t, topicBinding.SessionID, restarted.SessionID)
	require.Equal(t, topicBinding.SessionUID, restarted.SessionUID)
	readRemoteComputerSession(t, massd, restarted, f.dir, "cua.yaml")
	p.stop(t, false)

	write(t, f.config, strings.Replace(config, "configFile: ./cua.yaml", "configFile: ./changed-cua.yaml", 1))
	p = f.start()
	blocked := waitBinding(t, p, "topic", topicBinding.OwnerID, "blocked")
	require.Equal(t, topicBinding.ID, blocked.ID)
	require.Equal(t, "remote_computer_configuration_changed", blocked.LastError)
	require.Equal(t, routerBinding.SessionID, waitBinding(t, p, "router", "", "ready").SessionID)

	message := input("held-for-remote-replacement")
	message.Message.ThreadID, message.ReplyTarget = fixtureThreadID("held-for-remote-replacement"), nil
	source := submit(t, p, f, message)
	notice := peer.next(t, "turn/start")
	require.Equal(t, routerBinding.SessionID, notice.session)
	notice.answer <- nativeSubmitAnswer{complete: true}
	waitRouteStatus(t, p, source.EventID, "sent")
	route := newRoute("hold-for-remote-replacement", []string{source.MessageID}, f.dir)
	route.Targets[0].Create, route.Targets[0].TopicID = nil, topicBinding.OwnerID
	decision := mcpWork[core.RouteDecision](t, router, "route_messages", route)
	heldEvent := decision.Targets[0].EventIDs[source.MessageID]
	require.NotEmpty(t, heldEvent)
	require.Empty(t, waitWorkEvent(t, p, heldEvent, "pending").SentAt)
	peer.quiet(t, 200*time.Millisecond)

	operatorToken, err := os.ReadFile(operator)
	require.NoError(t, err)
	code, data := request(t, http.MethodPost, p.endpoint, fmt.Sprintf("/v1/core/session-bindings/%s/recoveries", blocked.ID),
		strings.TrimSpace(string(operatorToken)), map[string]any{
			"operationId": "retry-before-replace", "mode": "retry", "reconciliation": "Checked tool configuration",
			"acceptPossibleDuplicate": true,
		})
	require.Equal(t, http.StatusConflict, code, "%s", data)
	require.Contains(t, string(data), "remote computer configuration changed")

	recovery := recoveryFor("replace-remote-topic", "replace", blocked.ID)
	recovery.StopRuntime = true
	replaced := recoverRoleBinding(t, p, operator, recovery)
	require.NotEmpty(t, replaced.ReplacementSessionBindingID)
	fresh := waitBinding(t, p, "topic", topicBinding.OwnerID, "ready")
	require.Equal(t, replaced.ReplacementSessionBindingID, fresh.ID)
	require.NotEqual(t, topicBinding.SessionID, fresh.SessionID)
	readRemoteComputerSession(t, massd, fresh, f.dir, "changed-cua.yaml")
	resumed := peer.next(t, "turn/start")
	require.Equal(t, fresh.SessionID, resumed.session)
	batch := decodeInternalBatch(t, resumed)
	require.True(t, slices.ContainsFunc(batch, func(event internalEvent) bool { return event.ID == heldEvent }))
	resumed.answer <- nativeSubmitAnswer{complete: true}
	require.NotEmpty(t, waitWorkEvent(t, p, heldEvent, "sent").SentAt)
	recoveryEvents := workEvents(t, p, "binding_recovered")
	require.Len(t, recoveryEvents, 1)
	recoveryID := recoveryEvents[0].ID
	if !slices.ContainsFunc(batch, func(event internalEvent) bool { return event.ID == recoveryID }) {
		next := peer.next(t, "turn/start")
		require.Equal(t, fresh.SessionID, next.session)
		decodeInternalBatch(t, next, recoveryID)
		next.answer <- nativeSubmitAnswer{complete: true}
	}
	waitWorkEvent(t, p, recoveryID, "sent")
	var previous resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: topicBinding.SessionID}, &previous))
	require.Equal(t, "offline", string(previous.Status.State))
	require.Equal(t, originalTopicSession.Spec, previous.Spec)
}
