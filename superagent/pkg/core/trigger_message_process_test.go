package core_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestCoreRejectsRemovedConfigurationFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.yaml")
	for _, field := range []string{
		"accessToken: {triggers: ./old.token}",
		"triggerTokenFile: ./old.token",
		"sources: [{source: old}]",
		"outboxConsumers: [{source: example}]",
	} {
		t.Run(field, func(t *testing.T) {
			payload := "listen: 127.0.0.1:0\n" + field + "\n"
			write(t, path, payload)
			_, err := core.LoadConfig(path)
			require.ErrorContains(t, err, "unknown or invalid fields")
			_, err = core.LoadComposeConfig(path, strings.NewReader(payload))
			require.ErrorContains(t, err, "unknown or invalid fields")
		})
	}
}

func TestSharedTriggerIdentityContract(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	const path = "/apis/core/v1/trigger-messages"
	message := input("native-id")
	message.Message.ThreadID = "unregistered/Thread%2F入口"
	identity := core.Source{Source: "unregistered", AccountID: "account-a"}

	for _, tc := range []struct {
		name, token, code string
		identity          core.Source
		status            int
	}{
		{"missing token", "", "unauthorized", identity, http.StatusUnauthorized},
		{"wrong token", strings.Repeat("w", 48), "unauthorized", identity, http.StatusUnauthorized},
		{"missing headers", f.token, "invalid_argument", core.Source{}, http.StatusBadRequest},
		{"missing account", f.token, "invalid_argument", core.Source{Source: identity.Source}, http.StatusBadRequest},
		{"missing source", f.token, "invalid_argument", core.Source{AccountID: identity.AccountID}, http.StatusBadRequest},
		{"invalid source", f.token, "invalid_argument", core.Source{Source: "bad/source", AccountID: identity.AccountID}, http.StatusBadRequest},
		{"invalid account", f.token, "invalid_argument", core.Source{Source: identity.Source, AccountID: "bad,account"}, http.StatusBadRequest},
		{"reserved source", f.token, "forbidden", core.Source{Source: "core:scheduled-task", AccountID: identity.AccountID}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := request(t, http.MethodPost, p.endpoint, path, tc.token, message, tc.identity)
			errorCode(t, status, raw, tc.status, tc.code)
			status, raw = request(t, http.MethodGet, p.endpoint, path+"?sourceMessageId=native-id", tc.token, nil, tc.identity)
			errorCode(t, status, raw, tc.status, tc.code)
		})
	}

	receipts := make(map[core.Source]core.Receipt)
	for _, source := range []string{"unregistered", "another-source"} {
		for _, account := range []string{"account-a", "account-b"} {
			identity := core.Source{Source: source, AccountID: account}
			status, raw := request(t, http.MethodPost, p.endpoint, path, f.token, message, identity)
			require.Equal(t, http.StatusOK, status, string(raw))
			var receipt core.Receipt
			require.NoError(t, json.Unmarshal(raw, &receipt))
			require.False(t, receipt.Duplicate)
			require.Equal(t, message.Message.ThreadID, receipt.ThreadID)
			for _, earlier := range receipts {
				require.NotEqual(t, earlier.MessageID, receipt.MessageID)
			}
			receipts[identity] = receipt
		}
	}
	p.stop(t, false)
	p = f.start()
	for identity, original := range receipts {
		status, raw := request(t, http.MethodGet, p.endpoint, path+"?sourceMessageId=native-id", f.token, nil, identity)
		require.Equal(t, http.StatusOK, status, string(raw))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		require.Equal(t, original, receipt)
	}
	require.Len(t, page(t, p, "threads").Items, 1)
	require.Len(t, page(t, p, "trigger-messages").Items, 4)
}

func TestStructuredTriggerMessageProcess(t *testing.T) {
	f := fixtureAt(t)
	const thread = "opaque/Case%2f:入口"
	const ingress = "/apis/core/v1/trigger-messages"
	identity := func(source string) core.Source {
		return core.Source{Source: source, AccountID: source + "-account"}
	}
	config := f.executionWithUnavailableMassd()
	p := f.start()
	newInput := func(op, id string, blocks ...events.ContentBlock) core.SubmitTriggerMessageRequest {
		return core.SubmitTriggerMessageRequest{OperationID: op, Message: core.TriggerMessage{
			SourceMessageID: id, ThreadID: thread, Content: blocks,
		}}
	}
	submit := func(source string, input core.SubmitTriggerMessageRequest) core.Receipt {
		t.Helper()
		status, data := request(t, http.MethodPost, p.endpoint, ingress, f.token, input, identity(source))
		require.Equal(t, http.StatusOK, status, string(data))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(data, &receipt))
		return receipt
	}
	reject := func(source string, input any, status int, code string) {
		t.Helper()
		got, data := request(t, http.MethodPost, p.endpoint, ingress, f.token, input, identity(source))
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
		require.Equal(t, thread, receipt.ThreadID)
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
	reject("custom", replay, http.StatusConflict, "conflict")
	changedThread := newInput("second", "repeat-dws-id", events.Text("test"))
	submit("dws", changedThread)
	changedThread.Message.ThreadID = "opaque/another"
	reject("dws", changedThread, http.StatusConflict, "conflict")
	for _, invalid := range []string{
		`{"operationId":"o","message":{"sourceMessageId":"m","threadId":"x","content":[]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadId":"x","content":[{"type":"resource_link","uri":"file:///log"}]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadId":"x","content":[{"type":"text","text":"ok","group":true}]}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadId":"x","content":[{"type":"text","text":"ok"}],"_meta":null}}`,
		`{"operationId":"o","message":{"sourceMessageId":"m","threadId":"x","content":[{"type":"text","text":"ok"}],"source":"spoofed"}}`,
	} {
		reject("custom", json.RawMessage(invalid), http.StatusBadRequest, "invalid_argument")
	}
	stored := readRecord[core.Message](t, p, "trigger-messages/"+first.MessageID)
	require.Equal(t, "custom", stored.Source)
	require.Equal(t, "custom-account", stored.AccountID)
	require.JSONEq(t, string(firstInput.Message.Meta), string(stored.TriggerMeta))
	require.Len(t, page(t, p, "threads").Items, 1)
	require.Len(t, page(t, p, "trigger-messages").Items, 4)
	require.Len(t, routingEvents(t, p, first.MessageID), 1)

	// Restore the MASS endpoint so the first three committed events enter the
	// same selection window through the same Router binding.
	p.stop(t, false)
	boundary := newRouteBoundary(t)
	f.switchMassd(config, boundary.url)
	p = f.start()
	lookup := ingress + "?sourceMessageId=" + url.QueryEscape(firstInput.Message.SourceMessageID)
	status, raw := request(t, http.MethodGet, p.endpoint, lookup, f.token, nil, identity("custom"))
	require.Equal(t, http.StatusOK, status, string(raw))
	var readback core.Receipt
	require.NoError(t, json.Unmarshal(raw, &readback))
	require.Equal(t, first, readback)
	require.NotContains(t, string(raw), `"content"`)
	status, raw = request(t, http.MethodGet, p.endpoint, lookup, f.token, nil, identity("github"))
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NoError(t, json.Unmarshal(raw, &readback))
	require.Equal(t, second, readback)
	status, raw = request(t, http.MethodGet, p.endpoint, lookup, f.token, nil, identity("dws"))
	errorCode(t, status, raw, http.StatusNotFound, "not_found")
	status, raw = request(t, http.MethodGet, p.endpoint, lookup, "", nil)
	errorCode(t, status, raw, http.StatusUnauthorized, "unauthorized")
	status, raw = request(t, http.MethodHead, p.endpoint, lookup, f.token, nil, identity("custom"))
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, raw)
	status, raw = request(t, http.MethodGet, p.endpoint, lookup+"&view=router", "", nil)
	errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
	status, raw = request(t, http.MethodGet, p.endpoint, ingress+"?sourceMessageId=%zz", "", nil)
	errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
	status, raw = request(t, http.MethodGet, p.endpoint, ingress+"/"+first.MessageID, "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NotContains(t, string(raw), `"inReplyToOutboxId"`)
	status, raw = request(t, http.MethodGet, p.endpoint, ingress+"?view=router&messageId="+first.MessageID, "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var routedPage core.Page
	require.NoError(t, json.Unmarshal(raw, &routedPage))
	require.Len(t, routedPage.Items, 1)
	require.Contains(t, string(routedPage.Items[0]), first.MessageID)
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
	t.Log("Core HTTP, scoped MCP, SQLite and MASS SendPrompt preserved typed blocks and exact Thread IDs across distinct Trigger sources")
}
