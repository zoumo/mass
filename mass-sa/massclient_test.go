package sa

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMASSClientUsesJSONRPC2ForListPromptAndClose(t *testing.T) {
	fake := newFakeMASSServer(t, "")
	client, err := OpenMASS(t.Context(), MASSConfig{Endpoint: fake.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	created, err := client.CreateSession(t.Context(), CreateSessionInput{
		Metadata: SessionMetadata{Namespace: "mass-f-test", Name: "worker-one"},
		Spec: SessionSpec{
			RuntimeRef: "deepseek-claude",
			Cwd:        "/work/mass",
			Config:     &InitialSessionConfig{Model: "deepseek-worker", Effort: "high"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListSessions(t.Context(), SessionQuery{Namespace: "mass-f-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Status.SessionID != created.Status.SessionID {
		t.Fatalf("session.list result = %+v", page)
	}
	receipt, err := client.PromptSession(t.Context(), SessionRef{SessionID: created.Status.SessionID}, "review this")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receipt), `"sent":true`) {
		t.Fatalf("prompt receipt = %s", receipt)
	}
	closed, err := client.CloseSession(t.Context(), SessionRef{SessionID: created.Status.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status.State != "offline" {
		t.Fatalf("closed Session = %+v", closed)
	}

	_, closeCalls, promptCalls := fake.operationCounts()
	if closeCalls != 1 || promptCalls != 1 {
		t.Fatalf("close calls = %d, prompt calls = %d", closeCalls, promptCalls)
	}
	if len(fake.promptPayloads) != 1 {
		t.Fatalf("prompt payloads = %d", len(fake.promptPayloads))
	}
	var promptParams struct {
		Session SessionRef `json:"session"`
		Input   struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(fake.promptPayloads[0], &promptParams); err != nil {
		t.Fatal(err)
	}
	if promptParams.Session.SessionID != created.Status.SessionID ||
		len(promptParams.Input.Content) != 1 ||
		promptParams.Input.Content[0].Type != "text" ||
		promptParams.Input.Content[0].Text != "review this" {
		t.Fatalf("prompt wire shape = %+v", promptParams)
	}
}
