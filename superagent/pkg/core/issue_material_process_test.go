package core_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestStructuredIssueMaterialsProcess(t *testing.T) {
	f, boundary := fixtureAt(t), newRouteBoundary(t)
	const threadKey = "work/structured-material"
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, string(config)+"    threadKeys: ['"+threadKey+"']\n")
	f.execution(boundary)
	boundary.driver.mu.Lock()
	boundary.driver.injectReceipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}}
	boundary.driver.mu.Unlock()
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	submitMaterial := func(id string, content ...events.ContentBlock) core.Receipt {
		t.Helper()
		status, body := request(t, http.MethodPost, p.endpoint, "/v1/core/trigger-messages", f.token, core.SubmitTriggerMessageRequest{
			OperationID: id,
			Message: core.TriggerMessage{
				SourceMessageID: id, ThreadKey: threadKey, Content: content,
				Meta: json.RawMessage(`{"diagnostic":"adapter evidence"}`),
			},
		})
		require.Equal(t, http.StatusOK, status, string(body))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(body, &receipt))
		return receipt
	}
	attachment := events.ContentBlock{ResourceLink: &events.ContentBlockResourceLink{URI: "file:///checks.log", Name: "checks.log"}}
	mixed := []events.ContentBlock{events.Text("Check the attached log."), attachment}
	first := submitMaterial("initial-context", mixed...)
	waitDelivered(t, p, first.MessageID, "router", "")
	route := decide(t, p, roleToken(t, f, router), newRoute("material-topic", []string{first.MessageID}, f.dir))
	topicID := route.Targets[0].TopicID
	topic := waitBinding(t, p, "topic", topicID, "ready")
	topicToken := roleToken(t, f, topic)
	routedID := waitDelivered(t, p, first.MessageID, "topic", topicID)
	second := submitMaterial("attachment-only", attachment)
	bigContent := []events.ContentBlock{events.Text(strings.Repeat("界", 800)), attachment}
	oversize := submitMaterial("over-budget", bigContent...)
	unlinked := submitMaterial("unlinked", events.Text("For the Topic only."))
	nearLimit := events.Text(strings.Repeat("x", 1970))
	receivedMaterials := []core.Receipt{second, oversize, unlinked}
	var extraMaterials []core.Material
	for i := range 4 {
		extra := submitMaterial("near-limit-"+strconv.Itoa(i), nearLimit)
		receivedMaterials = append(receivedMaterials, extra)
		extraMaterials = append(extraMaterials, core.Material{MessageID: extra.MessageID})
	}
	for _, message := range receivedMaterials {
		waitDelivered(t, p, message.MessageID, "topic", topicID)
	}

	firstMaterial := core.Material{MessageID: first.MessageID}
	secondMaterial := core.Material{MessageID: second.MessageID}
	largeMaterial := core.Material{MessageID: oversize.MessageID}
	create := issueInput(f.dir, "structured-materials")
	create.Start = true
	create.Materials = append([]core.Material{firstMaterial, largeMaterial, secondMaterial}, extraMaterials...)
	work := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", create)
	issue := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	issueToken := roleToken(t, f, issue)
	received := func(id string) (internalEvent, []events.ContentBlock, int) {
		t.Helper()
		waitWorkEvent(t, p, id, "sent")
		boundary.driver.mu.Lock()
		defer boundary.driver.mu.Unlock()
		for _, input := range boundary.driver.inputs[issue.SessionID] {
			header := input.Content[0].Text.Text
			if strings.Contains(header, `"event_id":"`+id+`"`) {
				require.NotContains(t, header, "adapter evidence")
				return decodeInternalBatch(t, nativeSubmitCall{text: header}, id)[0], input.Content[1:], boundary.driver.injects[issue.SessionID]
			}
		}
		t.Fatalf("Issue Session did not receive event %s", id)
		return internalEvent{}, nil, 0
	}
	attached := []attachedMessage{{MessageID: first.MessageID, BlockCount: 2}, {MessageID: second.MessageID, BlockCount: 1}}
	startBlocks := []events.ContentBlock{mixed[0], attachment, attachment}
	for _, extra := range extraMaterials[:3] {
		attached = append(attached, attachedMessage{MessageID: extra.MessageID, BlockCount: 1})
		startBlocks = append(startBlocks, nearLimit)
	}
	event, blocks, injects := received(work.SessionBinding.StartEventID)
	require.Equal(t, "issue_session_started", event.Kind)
	require.Equal(t, attached, event.MessageContent)
	require.Empty(t, event.Messages)
	require.Equal(t, startBlocks, blocks)
	require.Zero(t, injects)
	var snapshot core.Issue
	require.NoError(t, json.Unmarshal(event.Issue, &snapshot))
	require.Equal(t, work.Issue.RequirementsRevision, snapshot.RequirementsRevision)
	require.Equal(t, work.Issue.Requirements, snapshot.Requirements)
	require.Equal(t, create.Materials, snapshot.Materials)

	full := roleWork[core.Message](t, p, issueToken, "get_message", map[string]string{"messageId": oversize.MessageID})
	require.Equal(t, bigContent, full.Content)
	require.Equal(t, threadKey, full.ThreadKey)
	require.Equal(t, "dws", full.Source)
	require.Empty(t, full.TriggerMeta)
	budgetFallback := roleWork[core.Message](t, p, issueToken, "get_message", map[string]string{"messageId": extraMaterials[3].MessageID})
	require.Equal(t, []events.ContentBlock{nearLimit}, budgetFallback.Content)
	status, body := requestMCPTool(t, p.endpoint, "get_message", issueToken, map[string]string{"messageId": unlinked.MessageID})
	errorCode(t, status, body, http.StatusNotFound, "not_found")

	continued := roleWork[core.WorkReceipt](t, p, topicToken, "issue_session_input", core.IssueSessionInputRequest{
		OperationID: "continue", ExpectedRevision: work.Issue.Revision, IssueID: work.Issue.ID, Mode: "continue",
	})
	event, blocks, injects = received(continued.NotificationEventID)
	require.Equal(t, "issue_session_update", event.Kind)
	require.Equal(t, attached, event.MessageContent)
	require.Equal(t, startBlocks, blocks)
	require.Zero(t, injects)

	supplement := roleWork[core.WorkReceipt](t, p, topicToken, "issue_session_input", core.IssueSessionInputRequest{
		OperationID: "supplement", ExpectedRevision: continued.Issue.Revision, IssueID: work.Issue.ID,
		Mode: "context_supplement", Materials: []core.Material{secondMaterial, largeMaterial},
	})
	event, blocks, injects = received(supplement.NotificationEventID)
	require.Equal(t, "context_supplement", event.Kind)
	require.Equal(t, attached[1:2], event.MessageContent)
	require.Equal(t, []events.ContentBlock{attachment}, blocks)
	require.Empty(t, event.Issue)
	require.Equal(t, 1, injects)

	diagnostic := readRecord[core.Message](t, p, "trigger-messages/"+second.MessageID)
	require.Equal(t, []events.ContentBlock{attachment}, diagnostic.Content)
	require.JSONEq(t, `{"diagnostic":"adapter evidence"}`, string(diagnostic.TriggerMeta))
	for _, preview := range []struct {
		path, field, id, expected string
	}{
		{"trigger-messages?preview=true", "text", first.MessageID, "Check the attached log."},
		{"trigger-messages?preview=true", "text", second.MessageID, "[resource_link]"},
		{"trigger-messages?view=router&messageId=" + first.MessageID, "text", first.MessageID, "Check the attached log."},
		{"topics/" + topicID + "/activity?view=conversation", "summary", "router_routed/" + routedID, "Check the attached log."},
	} {
		found := false
		for _, raw := range page(t, p, preview.path).Items {
			var item map[string]any
			require.NoError(t, json.Unmarshal(raw, &item))
			if item["id"] == preview.id {
				require.Equal(t, preview.expected, item[preview.field])
				require.NotContains(t, item, "_meta")
				require.NotContains(t, item, "content")
				found = true
			}
		}
		require.True(t, found, "%s in %s", preview.id, preview.path)
	}
	require.Len(t, page(t, p, "issues?messageId="+second.MessageID).Items, 1)
	require.Empty(t, page(t, p, "issues?messageId="+unlinked.MessageID).Items)
}
