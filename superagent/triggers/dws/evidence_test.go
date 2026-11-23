package dws

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportSentEvidenceMatchesAfterRestart(t *testing.T) {
	c := Component{config: dwsConfig{
		Source: "dws", AccountID: "account", InstanceID: "fixture", DatabasePath: filepath.Join(t.TempDir(), "dws.sqlite"),
	}}
	const row = `{"source":"dws","accountId":"account","outboxId":"reply","conversationId":"thread","messageId":"answer","taskId":"old-task"}`
	for range 2 {
		if err := c.ImportSentEvidence(strings.NewReader(row)); err != nil {
			t.Fatal(err)
		}
	}
	x := strings.Replace(row, `"messageId":"answer"`, `"messageId":"different"`, 1)
	if err := c.ImportSentEvidence(strings.NewReader(x)); err == nil {
		t.Fatal("changed an imported native identity")
	}
	evidence, err := openSendEvidence(c.config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	ref := nativeMessageRef{ConversationID: "thread", MessageID: "answer"}
	attempt, held := evidence.match(ref, time.Now())
	if held || !attempt.Settled || attempt.OutboxID != "reply" || attempt.Result.TaskID != "old-task" {
		t.Fatalf("imported evidence was not restored: %+v", attempt)
	}
}
