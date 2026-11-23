package core_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestStructuredTriggerMessageProcess(t *testing.T) {
	f := fixtureAt(t)
	const thread = "opaque/Case%2f:入口"
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config = []byte(strings.Replace(string(config), "['dws:']", "['opaque/']", 1))
	tokens := map[string]string{"dws": f.token}
	for _, source := range []string{"custom", "github", "antcode"} {
		tokens[source] = strings.Repeat(source, 16)
		file := filepath.Join(f.dir, source+".token")
		write(t, file, tokens[source])
		config = append(config, []byte("  - source: "+source+"\n    accountId: "+source+"-account\n    tokenFile: "+file+"\n    threadKeys: ['"+thread+"']\n")...)
	}
	write(t, f.config, string(config))
	p := f.start()
	newInput := func(op, id string, blocks ...events.ContentBlock) core.SubmitTriggerMessageRequest {
		return core.SubmitTriggerMessageRequest{OperationID: op, Message: core.TriggerMessage{
			SourceMessageID: id, ThreadKey: thread, Content: blocks,
		}}
	}
	submit := func(source string, input core.SubmitTriggerMessageRequest) core.Receipt {
		t.Helper()
		status, data := request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", tokens[source], input)
		require.Equal(t, http.StatusOK, status, string(data))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(data, &receipt))
		return receipt
	}
	reject := func(token string, input any, status int, code string) {
		t.Helper()
		got, data := request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", token, input)
		errorCode(t, got, data, status, code)
	}
	attachment := events.ContentBlock{ResourceLink: &events.ContentBlockResourceLink{URI: "file:///logs/checks.log", Name: "checks.log"}}
	firstInput := newInput("first", "shared-id", events.Text("Review https://github.example/org/repo/pull/42#comment"), attachment)
	firstInput.Message.Meta = json.RawMessage(`{"addressed":"group","source":"dws","replyTarget":{"where":7}}`)
	secondInput := newInput("second", "shared-id", events.Text("Another account observed the same Thread."))
	largeInput := newInput("large", "large-id", events.Text(strings.Repeat("long ", 700)), attachment)
	first := submit("custom", firstInput)
	second := submit("github", secondInput)
	large := submit("antcode", largeInput)
	require.NotEqual(t, first.MessageID, second.MessageID)
	for _, receipt := range []core.Receipt{first, second, large} {
		require.Equal(t, thread, receipt.ThreadKey)
		require.NotEmpty(t, receipt.EventID)
	}

	replay := firstInput
	replay.OperationID = "replayed"
	replay.Message.Meta = json.RawMessage(`{"addressed":"direct"}`)
	replayed := submit("custom", replay)
	require.True(t, replayed.Duplicate)
	replayed.Duplicate = false
	require.Equal(t, first, replayed)
	replay.Message.Content = []events.ContentBlock{attachment, firstInput.Message.Content[0]}
	reject(tokens["custom"], replay, http.StatusConflict, "conflict")
	changedKey := newInput("second", "repeat-dws-id", events.Text("test"))
	submit("dws", changedKey)
	changedKey.Message.ThreadKey = "opaque/another"
	reject(tokens["dws"], changedKey, http.StatusConflict, "conflict")
	changedKey.Message.ThreadKey = strings.ToLower(thread)
	reject(tokens["custom"], changedKey, http.StatusForbidden, "forbidden")
	for _, invalid := range []string{
		`{"operationId":"o","message":{"sourceMessageId":"m","threadKey":"x","content":[]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadKey":"x","content":[{"type":"resource_link","uri":"file:///log"}]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadKey":"x","content":[{"type":"text","text":"ok","group":true}]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadKey":"x","content":[{"type":"text","text":"ok"}],"_meta":null}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadKey":"x","content":[{"type":"text","text":"ok"}],"source":"spoofed"}}`,
	} {
		reject(tokens["custom"], json.RawMessage(invalid), http.StatusBadRequest, "invalid_argument")
	}
	reject("", firstInput, http.StatusUnauthorized, "unauthorized")
	stored := readRecord[core.Message](t, p, "messages/"+first.MessageID)
	require.Equal(t, "custom", stored.Source)
	require.Equal(t, "custom-account", stored.AccountID)
	require.JSONEq(t, string(firstInput.Message.Meta), string(stored.TriggerMeta))
	require.Len(t, page(t, p, "threads").Items, 1)
	require.Len(t, page(t, p, "messages").Items, 4)
	require.Len(t, routingEvents(t, p, first.MessageID), 1)

	// Reopen with execution configured so the first three committed events enter
	// the same selection window. Keep MASS transport and Service in the test path.
	p.stop(t, false)
	boundary := newRouteBoundary(t)
	f.execution(boundary)
	p = f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	for _, receipt := range []core.Receipt{first, second, large} {
		waitRouteStatus(t, p, receipt.EventID, "sent")
	}
	inputWithEvent := func(session, eventID string) runtime.Input {
		t.Helper()
		boundary.driver.mu.Lock()
		defer boundary.driver.mu.Unlock()
		for _, input := range boundary.driver.inputs[session] {
			if strings.Contains(input.Content[0].Text.Text, `"event_id":"`+eventID+`"`) {
				return input
			}
		}
		t.Fatalf("Session %s did not receive event %s", session, eventID)
		return runtime.Input{}
	}
	batch := inputWithEvent(router.SessionID, first.EventID)
	batchEvents := decodeInternalBatch(t, nativeSubmitCall{text: batch.Content[0].Text.Text})
	require.GreaterOrEqual(t, len(batchEvents), 3)
	require.Equal(t, first.EventID, batchEvents[0].ID)
	require.Equal(t, []attachedMessage{{MessageID: first.MessageID, BlockCount: 2}}, batchEvents[0].MessageContent)
	require.Equal(t, second.EventID, batchEvents[1].ID)
	require.Equal(t, []attachedMessage{{MessageID: second.MessageID, BlockCount: 1}}, batchEvents[1].MessageContent)
	require.Equal(t, large.EventID, batchEvents[2].ID)
	require.Empty(t, batchEvents[2].MessageContent)
	require.Equal(t, firstInput.Message.Content, batch.Content[1:3])
	require.Equal(t, secondInput.Message.Content, batch.Content[3:4])
	require.NotContains(t, batch.Content[0].Text.Text, "replyTarget")
	routed := decide(t, p, routerToken, newRoute("first-topic", []string{first.MessageID}, f.dir))
	topicID := routed.Targets[0].TopicID
	topic := waitBinding(t, p, "topic", topicID, "ready")
	topicToken := roleToken(t, f, topic)
	routedID := waitDelivered(t, p, first.MessageID, "topic", topicID)
	topicInput := inputWithEvent(topic.SessionID, routedID)
	require.Equal(t, firstInput.Message.Content, topicInput.Content[1:])
	ordinary := roleWork[core.Message](t, p, topicToken, "get_message", map[string]string{"messageId": first.MessageID})
	require.Equal(t, firstInput.Message.Content, ordinary.Content)
	require.Empty(t, ordinary.TriggerMeta)
	full := roleWork[core.Message](t, p, routerToken, "get_message", map[string]string{"messageId": large.MessageID})
	require.Equal(t, largeInput.Message.Content, full.Content)
	status, body := requestMCPTool(t, p.endpoint, "get_message", topicToken, map[string]string{"messageId": large.MessageID})
	errorCode(t, status, body, http.StatusNotFound, "not_found")

	follow := submit("dws", newInput("follow", "after-subscription", events.Text("A follow-on request.")))
	delivery := waitRouteStatus(t, p, follow.EventID, "sent")
	require.Equal(t, topicID, delivery.Recipient.TopicID)
	returned := roleCall(t, p, topicToken, "decline_message", core.DeclineMessageRequest{EventID: follow.EventID, Reason: "needs reassignment"})
	returnedID := readReturnID(t, returned)
	waitRouteStatus(t, p, returnedID, "sent")
	returnedBatch := inputWithEvent(router.SessionID, returnedID)
	returnedEvent := decodeInternalBatch(t, nativeSubmitCall{text: returnedBatch.Content[0].Text.Text}, returnedID)[0]
	require.Contains(t, returnedBatch.Content[0].Text.Text, `"round":2`)
	require.Equal(t, []attachedMessage{{MessageID: follow.MessageID, BlockCount: 1}}, returnedEvent.MessageContent)
	secondRoute := newRoute("second-topic", []string{follow.MessageID}, f.dir)
	secondRoute.Rounds[follow.MessageID] = 2
	secondTopicID := decide(t, p, routerToken, secondRoute).Targets[0].TopicID
	waitDelivered(t, p, follow.MessageID, "topic", secondTopicID)
	multi := submit("antcode", newInput("multi", "multiple-subscriptions", events.Text("Multiple candidate Topics.")))
	waitDelivered(t, p, multi.MessageID, "router", "")
	require.Len(t, routingEvents(t, p, multi.MessageID), 1)
	t.Log("Core HTTP, scoped MCP, SQLite and MASS SendPrompt preserved typed blocks and exact Thread keys across distinct Trigger sources")
}
