package sa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic audit history, separate from the public producer/consumer scenario
// below. It includes same-Thread replies that must never appear for a peer.
func seedObserverOutputFixture(t *testing.T, config Config) {
	t.Helper()
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		for _, input := range []struct{ id, title, thread string }{
			{"output-origin", "回复与业务结果示例", "out-thread"},
			{"output-peer", "同 Thread 的另一请求", "out-thread"},
			{"output-empty", "尚无显式回复", "out-empty"},
		} {
			p.Triggers[input.id] = TriggerState{TriggerReceipt: TriggerReceipt{TriggerID: input.id, ProjectID: "alpha", Source: "fixture/account", SourceEventID: input.id, ThreadID: input.thread, AcceptedAt: at}, Kind: "message", Summary: input.title, Content: json.RawMessage(`{}`), TicketIDs: []string{"ticket-a"}, Decision: "associated"}
		}
		target := OutputTarget{Source: "fixture/account", ThreadID: "out-thread"}
		second := OutputTarget{Source: "fixture/second", ThreadID: "destination-2"}
		appendOutput(p, OutputRecord{ID: "reply/output-origin/early", ProjectID: "alpha", TriggerID: "output-origin", TicketID: "ticket-a", Kind: "reply.accepted", Summary: "已确认接单；尚未确认执行或完成。", Targets: []OutputTarget{target}, RecordedAt: at})
		appendOutput(p, OutputRecord{ID: "fact/ticket-a/1", ProjectID: "alpha", TriggerID: "output-origin", TicketID: "ticket-a", Kind: "ticket.completed", Summary: "审核已接受，业务工作已完成。", Evidence: []string{"report:fixture-result"}, Targets: []OutputTarget{target, second}, RecordedAt: at.Add(time.Minute)})
		appendOutput(p, OutputRecord{ID: "reply/output-origin/summary", ProjectID: "alpha", TriggerID: "output-origin", TicketID: "ticket-a", FactID: "fact/ticket-a/1", Kind: "reply.summary", Summary: "显式结果摘要 <img src=https://invalid.example/output onerror=alert(1)> token=fixture-output-secret", Targets: []OutputTarget{target}, RecordedAt: at.Add(2 * time.Minute)})
		appendOutput(p, OutputRecord{ID: "reply/output-peer/answer", ProjectID: "alpha", TriggerID: "output-peer", Kind: "reply.clarification", Summary: "PEER_REPLY_MUST_STAY_SEPARATE", Targets: []OutputTarget{target}, RecordedAt: at})
		appendOutput(p, OutputRecord{ID: "fact/ticket-a/private", ProjectID: "alpha", TicketID: "ticket-a", Kind: "ticket.blocked", Summary: "NO_TARGET_FACT_MUST_STAY_SEPARATE", Targets: []OutputTarget{}, RecordedAt: at})
		appendOutput(p, OutputRecord{ID: "fact/ticket-b/foreign", ProjectID: "alpha", TicketID: "ticket-b", Kind: "ticket.failed", Summary: "UNASSOCIATED_FACT_MUST_STAY_SEPARATE", Targets: []OutputTarget{target}, RecordedAt: at})
		for _, kind := range []string{"final", "thinking", "tool_result", "reply.final"} {
			appendOutput(p, OutputRecord{ID: "internal/" + kind, ProjectID: "alpha", TriggerID: "output-origin", Kind: kind, Summary: "INTERNAL_" + kind, Targets: []OutputTarget{target}, RecordedAt: at})
		}
		p.Triggers["output-pagination"] = TriggerState{TriggerReceipt: TriggerReceipt{TriggerID: "output-pagination", ProjectID: "alpha", Source: "fixture/account", SourceEventID: "output-pagination", ThreadID: "page-thread", AcceptedAt: at.Add(-time.Hour)}, Kind: "message", Summary: "分页回复示例", Content: json.RawMessage(`{}`), Decision: "pending"}
		for i := range 51 {
			appendOutput(p, OutputRecord{ID: fmt.Sprintf("reply/output-pagination/%02d", i), ProjectID: "alpha", TriggerID: "output-pagination", Kind: "reply.no_execution", Summary: fmt.Sprintf("分页回复 %02d", i), Targets: []OutputTarget{{Source: "fixture/account", ThreadID: "page-thread"}}, RecordedAt: at})
		}
		confirmed := at.Add(3 * time.Minute)
		p.Consumers[consumerKey("adapter-a", target.Source, target.ThreadID)] = OutputConsumer{Cursor: 2, Observations: map[string]OutputObservation{
			"reply/output-origin/early": {ReadAt: at.Add(time.Second), Outcome: "delivered", Reference: "receipt-early", ConfirmedAt: at.Add(2 * time.Second)},
			"fact/ticket-a/1":           {ReadAt: at.Add(61 * time.Second), Outcome: "failed", Reference: "channel rejected", ConfirmedAt: confirmed},
		}}
		p.Consumers[consumerKey("resume-adapter", target.Source, target.ThreadID)] = OutputConsumer{Cursor: 1, Observations: map[string]OutputObservation{
			"reply/output-origin/early": {ReadAt: at.Add(time.Second), Outcome: "delivered", Reference: "resume receipt", ConfirmedAt: at.Add(2 * time.Second)},
		}}
		p.Consumers[consumerKey("adapter-b", target.Source, target.ThreadID)] = OutputConsumer{Observations: map[string]OutputObservation{
			"fact/ticket-a/1": {ReadAt: at.Add(62 * time.Second), Outcome: "unknown", Reference: "confirmation timeout", ConfirmedAt: confirmed},
		}}
		p.Consumers[consumerKey("adapter-a", second.Source, second.ThreadID)] = OutputConsumer{Observations: map[string]OutputObservation{
			"fact/ticket-a/1": {ReadAt: at.Add(63 * time.Second), Outcome: "delivered", Reference: "destination-2 receipt", ConfirmedAt: confirmed},
		}}
		return nil
	}))
}

func TestObserverOutputsPublicProjectionReadOnlyAndResume(t *testing.T) {
	config, mass := observerFixture(t)
	seedObserverOutputFixture(t, config)
	before, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	var original State
	require.NoError(t, json.Unmarshal(before, &original))
	base, configPath := startObserverProcess(t, config)
	// Reply badges use all exact-Trigger replies, independently of Output page
	// size, other requests in the same Thread, internal finals, or business facts.
	for id, count := range map[string]int{"output-origin": 2, "output-peer": 1, "output-empty": 0, "output-pagination": 51} {
		trigger := observerGet[ObserverTriggerDetail](t, base, "triggers/"+id+"?project=alpha")
		require.Equal(t, count, trigger.ReplyCount, id)
		page := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha&search="+id+"&limit=1")
		require.Len(t, page.Items, 1)
		require.Equal(t, count, page.Items[0].ReplyCount, id)
	}
	replies := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&trigger=output-origin&category=reply")
	require.Len(t, replies.Items, 2)
	require.Equal(t, "reply.accepted", replies.Items[0].Kind)
	require.Equal(t, "reply.summary", replies.Items[1].Kind)
	require.Empty(t, replies.Items[1].Audit)
	require.Contains(t, replies.Items[1].Summary, "[redacted]")
	require.NotContains(t, replies.Items[1].Summary, "fixture-output-secret")
	cli := runT06JSON[ObserverPage[ObserverOutput]](t, "observe", "outputs", "--config", configPath, "--project", "alpha", "--trigger", "output-origin", "--category", "reply")
	require.Equal(t, replies.Items, cli.Items)
	detail := observerGet[ObserverOutput](t, base, "outputs/"+url.PathEscape(replies.Items[0].ID)+"?project=alpha")
	require.Equal(t, replies.Items[0], detail)
	require.Equal(t, detail, runT06JSON[ObserverOutput](t, "observe", "outputs", detail.ID, "--config", configPath, "--project", "alpha"))
	facts := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&relatedTrigger=output-origin&category=fact")
	require.Len(t, facts.Items, 1)
	require.Equal(t, "ticket.completed", facts.Items[0].Kind)
	require.Len(t, facts.Items[0].Targets, 2)
	require.Len(t, facts.Items[0].Audit, 3)
	require.Equal(t, []string{"failed", "delivered", "unknown"}, []string{facts.Items[0].Audit[0].Outcome, facts.Items[0].Audit[1].Outcome, facts.Items[0].Audit[2].Outcome})
	for _, audit := range facts.Items[0].Audit {
		require.False(t, audit.ReadAt.IsZero())
		require.NotNil(t, audit.ConfirmedAt)
		require.NotEmpty(t, audit.Consumer)
		require.NotEmpty(t, audit.Source)
		require.NotEmpty(t, audit.ThreadID)
	}
	for _, query := range []string{
		"project=alpha&trigger=output-empty&category=reply",
		"project=beta", "project=alpha&source=fixture%2Faccount&thread=destination-2",
		"project=alpha&relatedTrigger=output-empty&category=fact",
	} {
		require.Empty(t, observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?"+query).Items)
	}
	require.Len(t, observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&ticket=ticket-a&category=fact").Items, 2)
	require.Len(t, observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?source=fixture%2Fsecond&thread=destination-2").Items, 1)
	for _, test := range []struct {
		resource string
		status   int
	}{
		{"outputs?project=missing", 404}, {"outputs?limit=201", 400},
		{"outputs?category=final", 400}, {"outputs?consumer=observer", 400},
		{"outputs?ticket=a&ticket=b", 400}, {"outputs?relatedTrigger=output-origin", 400},
		{"outputs?project=alpha&relatedTrigger=missing&category=fact", 404},
		{"outputs/" + url.PathEscape(detail.ID), 400},
		{"outputs/" + url.PathEscape(detail.ID) + "?project=beta", 404},
	} {
		response, err := http.Get(base + "/observer/api/" + test.resource)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, test.status, response.StatusCode, test.resource)
	}
	first := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&category=reply&limit=1")
	require.NotEmpty(t, first.Next)
	second := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&category=reply&limit=1&cursor="+url.QueryEscape(first.Next))
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	response, err := http.Get(base + "/observer/api/outputs?project=beta&category=reply&limit=1&cursor=" + url.QueryEscape(first.Next))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 400, response.StatusCode)
	// Multiple refreshes, a fresh process/connection and filtered traversals leave
	// both the file bytes and the decoded consumer/checkpoint state unchanged.
	for range 3 {
		_ = observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha")
		_ = observerGet[ObserverOutput](t, base, "outputs/"+url.PathEscape(facts.Items[0].ID)+"?project=alpha")
	}
	reopenedBase, reopenedConfig := startObserverProcess(t, configWithNewAddress(t, config))
	_ = runT06JSON[ObserverPage[ObserverOutput]](t, "observe", "outputs", "--config", reopenedConfig, "--project", "alpha")
	response, err = http.Get(reopenedBase + "/observer/api/outputs?project=alpha&category=reply&limit=1&cursor=" + url.QueryEscape(first.Next))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 400, response.StatusCode, "old traversal cannot survive process replacement")
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "read-only observation changed state bytes")
	var current State
	require.NoError(t, json.Unmarshal(after, &current))
	require.Equal(t, original, current)
	mass.mu.Lock()
	operations := slices.Clone(mass.operations)
	mass.mu.Unlock()
	require.Empty(t, operations, "output reads must not contact MASS")

	resumed := runT06JSON[OutputPage](t, "output", "read", "--config", configPath, "--project", "alpha", "--consumer", "resume-adapter", "--source", "fixture/account", "--thread", "out-thread")
	require.Equal(t, uint64(1), resumed.Checkpoint)
	require.Equal(t, "fact/ticket-a/1", resumed.Outputs[0].ID)
	require.Empty(t, resumed.Outputs[0].Observation.Outcome)
	require.Equal(t, "reply/output-origin/summary", resumed.Outputs[1].ID)
}

func TestObserverOutputsPublicProducersAndFrozenTraversal(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	base := "http://" + server.config.Management.Listen
	mass.setState(triggerSnapshot(t, server).Router.SessionID, "running")
	origin := acceptTestTrigger(t, server, "output-observe-origin", "observe-thread")
	peer := acceptTestTrigger(t, server, "output-observe-peer", "observe-thread")
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: origin.TriggerID, Action: "associate", TicketID: ticket.ID})
	early := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: origin.TriggerID, RequestID: "early", Kind: "accepted", TicketID: ticket.ID}))
	router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: peer.TriggerID, RequestID: "peer", Kind: "no_execution", Text: "Different Trigger in same Thread"})
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "observer-output-worker"}))
	require.NotEmpty(t, worker.SessionID)
	waitSessionReports(t, server, ticket, 1)
	observed := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&trigger="+origin.TriggerID+"&category=reply")
	require.Len(t, observed.Items, 1, "Worker final/tool result must not publish a reply")
	require.Equal(t, early.ID, observed.Items[0].ID)
	first := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&category=reply&limit=1")
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: ticket.ID, Resolution: "completed", Summary: "Explicit reviewed completion"})
	facts := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&relatedTrigger="+origin.TriggerID+"&category=fact")
	require.Len(t, facts.Items, 1)
	require.Equal(t, "ticket.completed", facts.Items[0].Kind)
	summary := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: origin.TriggerID, RequestID: "summary", Kind: "summary", TicketID: ticket.ID, FactID: facts.Items[0].ID, Text: "Reviewed result summary"}))
	next := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&category=reply&limit=1&cursor="+url.QueryEscape(first.Next))
	require.Equal(t, "reply/"+peer.TriggerID+"/peer", next.Items[0].ID)
	require.NotEqual(t, summary.ID, next.Items[0].ID)
	require.Empty(t, next.Next, "new reply must not enter the frozen traversal")
	require.Len(t, observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&trigger="+origin.TriggerID+"&category=reply").Items, 2)
	// Exercise the original public consumer contract, then verify independent
	// per-consumer audit and that later confirmations cannot rewrite old pages.
	configPath := outputTestConfig(t, server)
	for i, outcome := range []string{"failed", "unknown", "delivered"} {
		consumer := []string{"failed-adapter", "unknown-adapter", "delivered-adapter"}[i]
		page := runT06JSON[OutputPage](t, "output", "read", "--config", configPath, "--project", "mass", "--consumer", consumer, "--source", "test/account", "--thread", "observe-thread")
		require.NotEmpty(t, page.Outputs)
		runT06CLI(t, "output", "ack", facts.Items[0].ID, "--config", configPath, "--project", "mass", "--consumer", consumer, "--source", "test/account", "--thread", "observe-thread", "--outcome", outcome, "--reference", "fixture channel confirmation")
	}
	confirmed := observerGet[ObserverOutput](t, base, "outputs/"+url.PathEscape(facts.Items[0].ID)+"?project=mass")
	require.Len(t, confirmed.Audit, 3)
	require.Empty(t, facts.Items[0].Audit, "prior observation must not mutate with later acknowledgements")
	frozen := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&category=reply&limit=1&cursor="+url.QueryEscape(first.Next))
	require.Equal(t, next.Items, frozen.Items, "audit records are frozen along with content for this traversal")
	require.Equal(t, "completed", triggerSnapshot(t, server).Tickets[ticket.ID].BusinessState)
}
