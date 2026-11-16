package sa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateStoreWritesAtomicallyAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store, err := OpenStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	router := RouterState{
		SessionRef:    SessionRef{SessionID: "native", Namespace: RouterNamespace, Name: "mass-router"},
		RuntimeRef:    "deepseek-claude",
		Model:         "deepseek-router",
		Effort:        "low",
		ObservedState: "idle",
		UpdatedAt:     time.Unix(100, 0).UTC(),
	}
	if err := store.SetRouter("mass", router); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRouter("mass-other", RouterState{
		SessionRef:    SessionRef{SessionID: "native-2", Namespace: RouterNamespace, Name: "mass-other-router"},
		RuntimeRef:    "deepseek-claude",
		Effort:        "low",
		ObservedState: "idle",
		UpdatedAt:     time.Unix(200, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("state mode = %o, want %o", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != stateVersion || decoded.Projects["mass"].Router.SessionID != "native" {
		t.Fatalf("unexpected persisted state: %+v", decoded)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain: %v", temps)
	}

	reloaded, err := OpenStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reloaded.Snapshot()
	if snapshot.Projects["mass-other"].Router.SessionID != "native-2" {
		t.Fatalf("reloaded state lost project: %+v", snapshot)
	}
	snapshot.Projects["mass"] = ProjectState{}
	if reloaded.Snapshot().Projects["mass"].Router == nil {
		t.Fatal("Snapshot returned shared mutable state")
	}
}

func TestStateStoreKeepsRootAndTicketRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.updateProject("mass", func(project *ProjectState) error {
		project.Tickets["t-root"] = TicketState{
			ID:        "t-root",
			ProjectID: "mass",
			Title:     "Root",
			Objective: "Objective.",
			CreatedAt: time.Unix(300, 0).UTC(),
		}
		project.Tickets["t-12345678"] = TicketState{
			ID:             "t-12345678",
			ProjectID:      "mass",
			RootTicketID:   "t-root",
			ParentTicketID: "t-root",
			Title:          "Ticket",
			Objective:      "Objective.",
			Acceptance:     "Acceptance.",
			AssignmentID:   "a-12345678",
			Reports: []ReportState{{
				AssignmentID: "a-12345678",
				Session:      SessionRef{SessionID: "native", Namespace: "mass-t-root", Name: "worker"},
				ID:           "r-12345678",
				Body:         "Full report body.",
				ReportedAt:   time.Unix(400, 0).UTC(),
			}},
			CreatedAt: time.Unix(300, 0).UTC(),
			UpdatedAt: time.Unix(400, 0).UTC(),
		}
		project.Deliveries["b-12345678"] = DeliveryBatchState{
			ID:                 "b-12345678",
			ProjectID:          "mass",
			RootTicketID:       "t-root",
			TargetAssignmentID: "a-12345678",
			TargetWorkerName:   "worker",
			TargetSession:      SessionRef{SessionID: "native", Namespace: "mass-t-root", Name: "worker"},
			RoutingPath:        "direct",
			EventIDs:           []string{"e-1", "e-2"},
			PromptBytes:        123,
			State:              "delivered",
			CreatedAt:          time.Unix(400, 0).UTC(),
			UpdatedAt:          time.Unix(500, 0).UTC(),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.SetRouter("mass", RouterState{
		SessionRef:    SessionRef{SessionID: "native-router", Namespace: RouterNamespace, Name: "mass-router"},
		RuntimeRef:    "deepseek-claude",
		Effort:        "low",
		ObservedState: "idle",
		UpdatedAt:     time.Unix(500, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := OpenStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	project := reloaded.Snapshot().Projects["mass"]
	if project.Tickets["t-root"].Title != "Root" {
		t.Fatalf("root record was lost: %+v", project.Tickets)
	}
	if project.Tickets["t-12345678"].ParentTicketID != "t-root" ||
		len(project.Tickets["t-12345678"].Reports) != 1 {
		t.Fatalf("ticket record was lost: %+v", project.Tickets)
	}
	if got := project.Deliveries["b-12345678"]; got.State != "delivered" || len(got.EventIDs) != 2 {
		t.Fatalf("delivery record was lost: %+v", got)
	}

	snapshot := reloaded.Snapshot()
	ticket := snapshot.Projects["mass"].Tickets["t-12345678"]
	ticket.Reports[0].Body = "mutated"
	snapshot.Projects["mass"].Tickets["t-12345678"] = ticket
	if reloaded.Snapshot().Projects["mass"].Tickets["t-12345678"].Reports[0].Body != "Full report body." {
		t.Fatal("Snapshot returned a shared report slice")
	}
	delivery := snapshot.Projects["mass"].Deliveries["b-12345678"]
	delivery.EventIDs[0] = "mutated"
	snapshot.Projects["mass"].Deliveries["b-12345678"] = delivery
	if reloaded.Snapshot().Projects["mass"].Deliveries["b-12345678"].EventIDs[0] != "e-1" {
		t.Fatal("Snapshot returned a shared delivery event slice")
	}
}
