package sa

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapCreatesIndependentProjectRouters(t *testing.T) {
	t.Setenv("MASS_TEST_TOKEN", "not-a-real-token")
	fake := newFakeMASSServer(t, "not-a-real-token")
	mass := newTestProject("mass")
	secondaryProject := newTestProject("mass-other")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), mass, secondaryProject)
	cfg.MASS.TokenEnv = "MASS_TEST_TOKEN"
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	statuses, err := BootstrapRouters(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses = %d, want 2", len(statuses))
	}
	if statuses[0].ProjectID != "mass" || statuses[0].Model != "deepseek-router-mass" || statuses[0].Effort != "low" {
		t.Fatalf("unexpected first router status: %+v", statuses[0])
	}
	if statuses[1].ProjectID != "mass-other" || statuses[1].Model != "deepseek-router-mass-other" {
		t.Fatalf("unexpected second router status: %+v", statuses[1])
	}

	initialize, get, create, resume := fake.counts()
	if initialize != 1 || get != 2 || create != 2 || resume != 0 {
		t.Fatalf("calls init/get/create/resume = %d/%d/%d/%d", initialize, get, create, resume)
	}
	creates := fake.creates()
	if len(creates) != 2 {
		t.Fatalf("create payloads = %d, want 2", len(creates))
	}
	for i, raw := range creates {
		var params struct {
			Input CreateSessionInput `json:"input"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatal(err)
		}
		project := []ProjectConfig{mass, secondaryProject}[i]
		if params.Input.Metadata.Namespace != RouterNamespace || params.Input.Metadata.Name != RouterName(project) {
			t.Fatalf("unexpected router address: %+v", params.Input.Metadata)
		}
		if params.Input.Spec.RuntimeRef != project.RuntimeRef || params.Input.Spec.Cwd != project.CWD {
			t.Fatalf("unexpected router spec: %+v", params.Input.Spec)
		}
		if params.Input.Spec.Config == nil || params.Input.Spec.Config.Model != project.Router.Model || params.Input.Spec.Config.Effort != "low" {
			t.Fatalf("unexpected router config: %+v", params.Input.Spec.Config)
		}
		if params.Input.InitialInput == nil || len(params.Input.InitialInput.Content) != 1 ||
			!strings.Contains(params.Input.InitialInput.Content[0].Text, "Initialization only") ||
			!strings.Contains(params.Input.InitialInput.Content[0].Text, "MASS_SA_READY") {
			t.Fatalf("router create-only initialization input = %+v", params.Input.InitialInput)
		}
		if !strings.Contains(params.Input.Spec.SystemPrompt, "Super Agent router") ||
			!strings.Contains(params.Input.Spec.SystemPrompt, project.Memory) {
			t.Fatalf("router prompt omitted role or project memory: %q", params.Input.Spec.SystemPrompt)
		}
	}

	snapshot := store.Snapshot()
	if snapshot.Projects["mass"].Router.SessionID != statuses[0].SessionID ||
		snapshot.Projects["mass-other"].Router.SessionID != statuses[1].SessionID {
		t.Fatalf("router references were not persisted: %+v", snapshot)
	}
	if len(statuses[0].InitialInputReceipt) == 0 ||
		len(snapshot.Projects["mass"].Router.InitialInputReceipt) == 0 {
		t.Fatalf("router initialization receipt was not retained: status=%+v state=%+v", statuses[0], snapshot.Projects["mass"].Router)
	}
}

func TestBootstrapWaitsForCreateOnlyInitialization(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	fake.setCreateResponseState("unknown")
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	statuses, err := BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != "idle" {
		t.Fatalf("statuses = %+v, want one settled router", statuses)
	}
	if len(statuses[0].InitialInputReceipt) == 0 {
		t.Fatal("create-only initialization receipt was not retained")
	}
	_, _, create, resume := fake.counts()
	if create != 1 || resume != 0 {
		t.Fatalf("create/resume = %d/%d, want 1/0", create, resume)
	}
}

func TestBootstrapAdoptsExistingNamedRouterWithoutDuplicate(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	fake.addSession(Session{
		Metadata: SessionMetadata{Namespace: RouterNamespace, Name: RouterName(project)},
		Spec:     desiredRouterSpec(project),
		Status:   SessionStatus{SessionID: "existing-native", State: "idle"},
	})

	statuses, err := BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].SessionID != "existing-native" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	_, _, create, _ := fake.counts()
	if create != 0 {
		t.Fatalf("created %d routers after finding an existing named Session", create)
	}
	if store.Snapshot().Projects["mass"].Router.SessionID != "existing-native" {
		t.Fatal("adopted router reference was not persisted")
	}
}

func TestBootstrapExplicitlyResumesOfflineRouter(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	fake.setState(statuses[0].SessionID, "offline")

	statuses, err = BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].State != "idle" {
		t.Fatalf("resumed state = %q, want idle", statuses[0].State)
	}
	_, _, create, resume := fake.counts()
	if create != 1 || resume != 1 {
		t.Fatalf("create/resume = %d/%d, want 1/1", create, resume)
	}
	if observed := store.Snapshot().Projects["mass"].Router.ObservedState; observed != "idle" {
		t.Fatalf("persisted observed state = %q, want idle", observed)
	}
}

func TestBootstrapRejectsUnknownRouterWithoutRecreation(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	fake.setState(statuses[0].SessionID, "unknown")

	_, err = BootstrapRouters(t.Context(), cfg, store)
	if err == nil {
		t.Fatal("BootstrapRouters succeeded for unknown router, want error")
	}
	reason, _ := errorReason(err)
	if reason != "router_session_unknown" {
		t.Fatalf("reason = %q, want router_session_unknown: %v", reason, err)
	}
	_, _, create, resume := fake.counts()
	if create != 1 || resume != 0 {
		t.Fatalf("create/resume = %d/%d, want 1/0", create, resume)
	}
	if observed := store.Snapshot().Projects["mass"].Router.ObservedState; observed != "unknown" {
		t.Fatalf("persisted observed state = %q, want unknown", observed)
	}
}

func TestBootstrapRejectsMemoryDrift(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapRouters(t.Context(), cfg, store); err != nil {
		t.Fatal(err)
	}
	cfg.Projects[0].Memory = "new memory that requires a new Session"

	_, err = BootstrapRouters(t.Context(), cfg, store)
	if err == nil {
		t.Fatal("BootstrapRouters succeeded after prompt drift, want error")
	}
	reason, _ := errorReason(err)
	if reason != "router_prompt_mismatch" {
		t.Fatalf("reason = %q, want router_prompt_mismatch: %v", reason, err)
	}
	_, _, create, _ := fake.counts()
	if create != 1 {
		t.Fatalf("create count = %d, want 1", create)
	}
}

func TestBootstrapDoesNotRetryUnknownCreateOutcome(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	fake.dropCreateReply = true
	project := newTestProject("mass")
	cfg := newTestConfig(t, fake.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BootstrapRouters(t.Context(), cfg, store)
	if err == nil || !IsErrorCode(err, "outcome_unknown") {
		t.Fatalf("error = %v, want outcome_unknown", err)
	}
	_, _, create, _ := fake.counts()
	if create != 1 {
		t.Fatalf("create count = %d, want 1", create)
	}
	if store.Snapshot().Projects["mass"].Router != nil {
		t.Fatal("unknown create outcome was persisted as confirmed")
	}

	fake.mu.Lock()
	fake.dropCreateReply = false
	fake.mu.Unlock()
	statuses, err := BootstrapRouters(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	_, _, create, _ = fake.counts()
	if create != 1 {
		t.Fatalf("create count after recovery = %d, want 1", create)
	}
}

func TestBootstrapReportsUnreachableMASS(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	endpoint := fake.endpoint()
	fake.server.Close()
	project := newTestProject("mass")
	cfg := newTestConfig(t, endpoint, filepath.Join(t.TempDir(), "state.json"), project)
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BootstrapRouters(t.Context(), cfg, store)
	if err == nil || !IsErrorCode(err, "unavailable") {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func errorReason(err error) (string, bool) {
	var operation *OperationError
	if errors.As(err, &operation) {
		return operation.Reason, true
	}
	return "", false
}
