package sa

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateTicketCreatesParentIssueAndPersistsReference(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()

	root, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
		ProjectID: "mass",
		Title:     "Super Agent demo",
		Objective: "Deliver the project-level Super Agent demo.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(root.ID, "t-") {
		t.Fatalf("root ID = %q, want t- prefix", root.ID)
	}
	if root.Issue.ID == "" || root.Issue.Identifier == "" {
		t.Fatalf("root issue reference is incomplete: %+v", root.Issue)
	}

	issue := fake.issue(root.Issue.ID)
	if issue.ParentIssueID != "" {
		t.Fatalf("root parent issue has parent %q", issue.ParentIssueID)
	}
	if issue.ProjectID != fake.project {
		t.Fatalf("issue project = %q, want %q", issue.ProjectID, fake.project)
	}
	if issue.Metadata[MetadataProject] != "mass" || issue.Metadata[MetadataTicketID] != root.ID {
		t.Fatalf("root metadata = %+v", issue.Metadata)
	}
	if issue.Metadata[MetadataAssignmentID] != "" {
		t.Fatalf("root issue carries ticket metadata: %+v", issue.Metadata)
	}

	for _, invocation := range fake.invocations() {
		if !hasArg(invocation, "--workspace-id="+fake.workspace) {
			t.Fatalf("invocation omitted explicit workspace: %v", invocation.Args)
		}
		if hasArgPrefix(invocation, "--assignee") {
			t.Fatalf("invocation assigned a Multica agent: %v", invocation.Args)
		}
	}
	creates := fake.invocationsOf("issue", "create")
	if len(creates) != 1 {
		t.Fatalf("create invocations = %d, want 1", len(creates))
	}
	if !hasArg(creates[0], "--project="+fake.project) || !hasArg(creates[0], "--description-file=description.md") {
		t.Fatalf("create arguments are incomplete: %v", creates[0].Args)
	}
	if creates[0].CWD == "" || creates[0].CWD == "." {
		t.Fatalf("create did not run in a dedicated working directory: %q", creates[0].CWD)
	}
	if len(fake.invocationsOf("issue", "get")) == 0 {
		t.Fatal("root creation was not confirmed by issue readback")
	}

	stored := fake.storedState()
	if stored.Tickets[root.ID].Issue.ID != root.Issue.ID {
		t.Fatalf("root reference was not persisted: %+v", stored.Tickets)
	}
}

func TestCreateTicketLinksParentAndRecordsAllMetadata(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()
	root := mustCreateRootTicket(t, bridge, fake)

	ticket, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID:      "mass",
		ParentTicketID: root.ID,
		Title:          "Bridge Multica tickets",
		Objective:      "Bridge the ticket lifecycle to Multica.",
		Acceptance:     "Parent, metadata, comments, and status are authoritative.",
	})
	if err != nil {
		t.Fatal(err)
	}

	ticket, err = bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-11111111"})
	if err != nil {
		t.Fatal(err)
	}

	create := fake.invocationsOf("issue", "create")[1]
	if !hasArg(create, "--parent="+root.Issue.ID) {
		t.Fatalf("ticket create omitted parent: %v", create.Args)
	}
	issue := fake.issue(ticket.Issue.ID)
	if issue.ParentIssueID != root.Issue.ID {
		t.Fatalf("ticket parent = %q, want %q", issue.ParentIssueID, root.Issue.ID)
	}
	wantMetadata := map[string]string{
		MetadataProject:      "mass",
		MetadataTicketID:     ticket.ID,
		MetadataAssignmentID: "a-11111111",
	}
	for key, value := range wantMetadata {
		if issue.Metadata[key] != value {
			t.Fatalf("ticket metadata %s = %q, want %q (%+v)", key, issue.Metadata[key], value, issue.Metadata)
		}
	}
	if !strings.Contains(issue.Description, "Bridge the ticket lifecycle") ||
		!strings.Contains(issue.Description, "Parent, metadata, comments") {
		t.Fatalf("ticket description omitted objective or acceptance: %q", issue.Description)
	}

	stored := fake.storedState().Tickets[ticket.ID]
	if stored.RootTicketID != root.ID || stored.AssignmentID != "a-11111111" || stored.Issue.ID != ticket.Issue.ID {
		t.Fatalf("ticket was not persisted correctly: %+v", stored)
	}
}

func TestWorkerReportBecomesCommentWithAssignmentAndSession(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()
	root := mustCreateRootTicket(t, bridge, fake)
	ticket := mustCreateTicket(t, bridge, root.ID)

	if _, err := bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-22222222"}); err != nil {
		t.Fatal(err)
	}
	updated, err := bridge.AddReport(t.Context(), AddReportRequest{
		ProjectID:    "mass",
		TicketID:     ticket.ID,
		AssignmentID: "a-22222222",
		Session:      SessionRef{SessionID: "native-worker-1", Namespace: "mass-f-abc123", Name: "worker-one"},
		Body:         "Implemented the bridge and ran the tests.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(updated.Reports))
	}
	if updated.Reports[0].CommentID == "" || updated.Reports[0].Session.Name != "worker-one" {
		t.Fatalf("report reference is incomplete: %+v", updated.Reports[0])
	}
	if updated.Status != "todo" {
		t.Fatalf("report changed ticket status to %q", updated.Status)
	}
	if len(fake.invocationsOf("issue", "status")) != 0 {
		t.Fatal("worker report changed the issue status")
	}

	issue := fake.issue(ticket.Issue.ID)
	if issue.Metadata[MetadataAssignmentID] != "a-22222222" {
		t.Fatalf("assignment metadata = %q", issue.Metadata[MetadataAssignmentID])
	}
	comments := fake.comments(ticket.Issue.ID)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(comments))
	}
	for _, fragment := range []string{"Worker report", "a-22222222", "mass-f-abc123/worker-one", "native-worker-1", "Implemented the bridge"} {
		if !strings.Contains(comments[0].Content, fragment) {
			t.Fatalf("comment omitted %q: %q", fragment, comments[0].Content)
		}
	}
	if len(fake.invocationsOf("issue", "comment", "list")) == 0 {
		t.Fatal("report comment was not confirmed by readback")
	}
}

func TestWorkerReportCannotProvisionAnAssignment(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()
	root := mustCreateRootTicket(t, bridge, fake)
	before := len(fake.invocations())
	_, err := bridge.AddReport(t.Context(), AddReportRequest{ProjectID: "mass", TicketID: root.ID, AssignmentID: "a-unprovisioned", Session: SessionRef{SessionID: "native", Namespace: "mass-root", Name: "worker"}, Body: "A report cannot create its own authority"})
	if reason, _ := errorReason(err); reason != "assignment_mismatch" {
		t.Fatalf("unprovisioned report = %v", err)
	}
	if len(fake.invocations()) != before || fake.storedState().Tickets[root.ID].AssignmentID != "" {
		t.Fatal("unprovisioned report changed business state or tracker")
	}
}

func TestFinishTicketMapsResolutionsAndRequiresNoStart(t *testing.T) {
	cases := map[TicketResolution]struct {
		status           string
		resolutionInMeta string
	}{
		ResolutionCompleted: {status: "done", resolutionInMeta: "completed"},
		ResolutionFailed:    {status: "done", resolutionInMeta: "failed"},
		ResolutionCancelled: {status: "done", resolutionInMeta: "cancelled"},
		ResolutionBlocked:   {status: "blocked", resolutionInMeta: "blocked"},
	}
	for resolution, want := range cases {
		t.Run(string(resolution), func(t *testing.T) {
			fake := newFakeMultica(t)
			bridge := fake.bridge()
			root := mustCreateRootTicket(t, bridge, fake)
			ticket := mustCreateTicket(t, bridge, root.ID)
			if _, err := bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{
				ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-33333333",
			}); err != nil {
				t.Fatal(err)
			}

			finished, err := bridge.FinishTicket(t.Context(), FinishTicketRequest{
				ProjectID:  "mass",
				TicketID:   ticket.ID,
				Resolution: resolution,
				Summary:    "Reviewed the report and accepted the outcome.",
			})
			if err != nil {
				t.Fatal(err)
			}
			if finished.BusinessState != string(resolution) || finished.Status != want.status {
				t.Fatalf("finished ticket = %+v, want resolution %s status %s", finished, resolution, want.status)
			}
			issue := fake.issue(ticket.Issue.ID)
			if issue.Status != want.status {
				t.Fatalf("issue status = %q, want %q", issue.Status, want.status)
			}
			if issue.Metadata[MetadataResolution] != want.resolutionInMeta {
				t.Fatalf("resolution metadata = %q, want %q", issue.Metadata[MetadataResolution], want.resolutionInMeta)
			}
			comments := fake.comments(ticket.Issue.ID)
			if len(comments) != 1 || !strings.Contains(comments[0].Content, string(resolution)) ||
				!strings.Contains(comments[0].Content, "Reviewed the report") {
				t.Fatalf("resolution comment is incomplete: %+v", comments)
			}

			statusCalls := fake.invocationsOf("issue", "status")
			if len(statusCalls) != 1 {
				t.Fatalf("status invocations = %d, want 1", len(statusCalls))
			}
			if !hasArg(statusCalls[0], "--no-start") {
				t.Fatalf("status call omitted --no-start: %v", statusCalls[0].Args)
			}
			invocations := fake.invocations()
			readbackAfterStatus := false
			for index, invocation := range invocations {
				if containsSequence(invocation.Args, []string{"issue", "status"}) {
					for _, later := range invocations[index+1:] {
						if containsSequence(later.Args, []string{"issue", "get"}) {
							readbackAfterStatus = true
						}
					}
				}
			}
			if !readbackAfterStatus {
				t.Fatal("status change was not confirmed by readback")
			}

			before := len(fake.invocations())
			repeatedResolution := ResolutionCompleted
			wantReason := "ticket_already_resolved"
			if resolution == ResolutionBlocked {
				repeatedResolution = ResolutionBlocked
				wantReason = "ticket_state_unchanged"
			}
			_, err = bridge.FinishTicket(t.Context(), FinishTicketRequest{
				ProjectID:  "mass",
				TicketID:   ticket.ID,
				Resolution: repeatedResolution,
				Summary:    "Second attempt.",
			})
			if err == nil {
				t.Fatal("second finish succeeded, want rejection")
			}
			if reason, _ := errorReason(err); reason != wantReason {
				t.Fatalf("reason = %q, want %s", reason, wantReason)
			}
			if len(fake.invocations()) != before {
				t.Fatal("rejected finish still called the Multica CLI")
			}
		})
	}
}

func TestRecordAssignmentReplacesOnlyTheCurrentAssignment(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()
	root := mustCreateRootTicket(t, bridge, fake)
	ticket := mustCreateTicket(t, bridge, root.ID)

	updated, err := bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{
		ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-44444444",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AssignmentID != "a-44444444" {
		t.Fatalf("assignment = %q", updated.AssignmentID)
	}
	if _, err := bridge.AddReport(t.Context(), AddReportRequest{
		ProjectID:    "mass",
		TicketID:     ticket.ID,
		AssignmentID: "a-stale",
		Session:      SessionRef{SessionID: "native", Namespace: "mass-f-a", Name: "worker"},
		Body:         "Stale report.",
	}); err == nil {
		t.Fatal("stale assignment report succeeded, want rejection")
	} else if reason, _ := errorReason(err); reason != "assignment_mismatch" {
		t.Fatalf("reason = %q, want assignment_mismatch", reason)
	}
}

func TestMulticaWriteFailuresNeverPersistCompletion(t *testing.T) {
	t.Run("command failure", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		t.Setenv("FAKE_MULTICA_FAIL", "create")

		_, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil {
			t.Fatal("CreateTicket succeeded, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_command_failed" {
			t.Fatalf("reason = %q, want multica_command_failed: %v", reason, err)
		}
		if len(fake.invocationsOf("issue", "create")) != 1 {
			t.Fatal("failed create was retried")
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("failed create persisted a root")
		}
	})

	t.Run("failure after remote write", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		t.Setenv("FAKE_MULTICA_FAIL", "create")
		t.Setenv("FAKE_MULTICA_FAIL_AFTER_WRITE", "1")

		_, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil {
			t.Fatal("CreateTicket succeeded, want error")
		}
		if len(fake.invocationsOf("issue", "create")) != 1 {
			t.Fatal("unconfirmed create was retried")
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("unconfirmed create was recorded as complete")
		}
	})

	t.Run("metadata readback mismatch", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		t.Setenv("FAKE_MULTICA_DROP_METADATA", MetadataTicketID)

		_, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil {
			t.Fatal("CreateTicket succeeded, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_readback_mismatch" {
			t.Fatalf("reason = %q, want multica_readback_mismatch: %v", reason, err)
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("metadata mismatch still persisted a root")
		}
	})

	t.Run("status readback mismatch", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		root := mustCreateRootTicket(t, bridge, fake)
		ticket := mustCreateTicket(t, bridge, root.ID)
		t.Setenv("FAKE_MULTICA_IGNORE_STATUS", "1")

		_, err := bridge.FinishTicket(t.Context(), FinishTicketRequest{
			ProjectID: "mass", TicketID: ticket.ID, Resolution: ResolutionCompleted, Summary: "Accept.",
		})
		if err == nil {
			t.Fatal("FinishTicket succeeded, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_readback_mismatch" {
			t.Fatalf("reason = %q, want multica_readback_mismatch: %v", reason, err)
		}
		stored := fake.storedState().Tickets[ticket.ID]
		if terminalBusinessState(stored.BusinessState) || stored.Status != "todo" {
			t.Fatalf("unconfirmed status was persisted: %+v", stored)
		}
	})

	t.Run("comment readback mismatch", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		root := mustCreateRootTicket(t, bridge, fake)
		ticket := mustCreateTicket(t, bridge, root.ID)
		t.Setenv("FAKE_MULTICA_OMIT_COMMENT", "1")
		if _, err := bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-55555555"}); err != nil {
			t.Fatal(err)
		}

		_, err := bridge.AddReport(t.Context(), AddReportRequest{
			ProjectID:    "mass",
			TicketID:     ticket.ID,
			AssignmentID: "a-55555555",
			Session:      SessionRef{SessionID: "native", Namespace: "mass-f-a", Name: "worker"},
			Body:         "Report.",
		})
		if err == nil {
			t.Fatal("AddReport succeeded, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_readback_mismatch" {
			t.Fatalf("reason = %q, want multica_readback_mismatch", reason)
		}
		if len(fake.storedState().Tickets[ticket.ID].Reports) != 0 {
			t.Fatal("unconfirmed comment was recorded as a report")
		}
	})

	t.Run("timeout is unknown", func(t *testing.T) {
		fake := newFakeMultica(t)
		bridge := fake.bridge()
		t.Setenv("FAKE_MULTICA_HANG", "create")
		ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancel()

		_, err := bridge.CreateTicket(ctx, CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil || !IsErrorCode(err, "outcome_unknown") {
			t.Fatalf("error = %v, want outcome_unknown", err)
		}
		if len(fake.invocationsOf("issue", "create")) != 1 {
			t.Fatal("unknown outcome was retried")
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("unknown outcome was recorded as complete")
		}
	})

	t.Run("missing executable", func(t *testing.T) {
		fake := newFakeMultica(t)
		cfg := fake.config()
		cfg.Multica.Executable = filepath.Join(t.TempDir(), "missing-multica")
		store, err := OpenStateStore(cfg.StateFile)
		if err != nil {
			t.Fatal(err)
		}
		bridge, err := NewTicketBridge(cfg, store)
		if err != nil {
			t.Fatal(err)
		}
		_, err = bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil {
			t.Fatal("CreateTicket succeeded, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_unavailable" {
			t.Fatalf("reason = %q, want multica_unavailable", reason)
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("unavailable CLI persisted a root")
		}
	})

	t.Run("wrong workspace is rejected", func(t *testing.T) {
		fake := newFakeMultica(t)
		cfg := fake.config()
		cfg.Projects[0].Multica.WorkspaceID = "00000000-0000-4000-8000-000000000000"
		store, err := OpenStateStore(cfg.StateFile)
		if err != nil {
			t.Fatal(err)
		}
		bridge, err := NewTicketBridge(cfg, store)
		if err != nil {
			t.Fatal(err)
		}
		_, err = bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
			ProjectID: "mass", Title: "Root", Objective: "Objective.",
		})
		if err == nil {
			t.Fatal("CreateTicket succeeded with the wrong workspace, want error")
		}
		if reason, _ := errorReason(err); reason != "multica_command_failed" {
			t.Fatalf("reason = %q, want multica_command_failed", reason)
		}
		if len(fake.storedState().Tickets) != 0 {
			t.Fatal("wrong workspace persisted a root")
		}
	})
}

func TestTicketBusinessValidationAvoidsExternalCalls(t *testing.T) {
	fake := newFakeMultica(t)
	bridge := fake.bridge()

	if _, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID: "mass", ParentTicketID: "f-missing", Title: "T", Objective: "O", Acceptance: "A",
	}); err == nil {
		t.Fatal("CreateTicket accepted an unknown root")
	} else if reason, _ := errorReason(err); reason != "ticket_not_found" {
		t.Fatalf("reason = %q, want ticket_not_found", reason)
	}
	if _, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
		ProjectID: "mass", Title: "T", Objective: "   ",
	}); err == nil {
		t.Fatal("CreateTicket accepted a blank objective")
	}
	if len(fake.invocations()) != 0 {
		t.Fatalf("validation failures called the CLI: %+v", fake.invocations())
	}

	root := mustCreateRootTicket(t, bridge, fake)
	ticket := mustCreateTicket(t, bridge, root.ID)
	if _, err := bridge.FinishTicket(t.Context(), FinishTicketRequest{
		ProjectID: "mass", TicketID: ticket.ID, Resolution: "unknown", Summary: "Summary.",
	}); err == nil {
		t.Fatal("FinishTicket accepted an invalid resolution")
	}
	if _, err := bridge.FinishTicket(t.Context(), FinishTicketRequest{
		ProjectID: "mass", TicketID: ticket.ID, Resolution: ResolutionCompleted, Summary: " ",
	}); err == nil {
		t.Fatal("FinishTicket accepted a blank summary")
	}
	if len(fake.invocationsOf("issue", "status")) != 0 {
		t.Fatal("invalid resolution changed the issue status")
	}
}

func mustCreateRootTicket(t *testing.T, bridge *TicketBridge, _ *fakeMultica) TicketState {
	t.Helper()
	root, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
		ProjectID: "mass",
		Title:     "Super Agent demo",
		Objective: "Deliver the project-level Super Agent demo.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustCreateTicket(t *testing.T, bridge *TicketBridge, rootTicketID string) TicketState {
	t.Helper()
	ticket, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID:      "mass",
		ParentTicketID: rootTicketID,
		Title:          "Bridge Multica tickets",
		Objective:      "Bridge the ticket lifecycle to Multica.",
		Acceptance:     "Parent, metadata, comments, and status are authoritative.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
