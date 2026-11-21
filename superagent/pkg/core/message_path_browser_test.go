package core_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

// Give the path scenarios their own Core so extra Topics, subscriptions and
// Issues cannot change the diagnostic and legacy workbench fixtures' totals.
func seedMessagePathObserver(t *testing.T) string {
	t.Helper()
	boundary := newRouteBoundary(t)
	f, _ := eventFixture(t, boundary.url)
	p := f.start()
	router := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
	setup := submit(t, p, f, input("observer-path-setup"))
	route := newRoute("path-setup", []string{setup.MessageID}, f.dir)
	route.Targets[0].Create.Title = "发布准备"
	first := decide(t, p, router, route).Targets[0].TopicID
	firstBinding := waitBinding(t, p, "topic", first, "ready")
	firstToken := roleToken(t, f, firstBinding)
	direct := submit(t, p, f, input("observer-path-direct"))
	waitDelivered(t, p, direct.MessageID, "topic", first)

	r := input("observer-path-rerouted")
	r.Message.Text = "发布前检查：请确认版本、回归结果与值班交接。"
	// Hold native acceptance until the Topic has returned the original delivery.
	m := func() core.Receipt {
		boundary.driver.mu.Lock()
		defer boundary.driver.mu.Unlock()
		msg := submit(t, p, f, r)
		roleCall(t, p, firstToken, "decline_message", core.DeclineMessageRequest{EventID: msg.EventID, Reason: "需要值班同学一起确认"})
		return msg
	}()
	ids := []string{m.MessageID}
	reroute := newRoute("path-reroute", ids, f.dir)
	reroute.Rounds[m.MessageID] = 2
	reroute.Reason = "发布准备与值班交接分别核对各自的检查项"
	reroute.Targets[0].Create.Title = "值班交接与发布后验证"
	reroute.Targets = append(reroute.Targets, core.RouteTarget{TopicID: first, MessageIDs: ids})
	decision := decide(t, p, router, reroute)
	second := decision.Targets[0].TopicID
	secondBinding := waitBinding(t, p, "topic", second, "ready")
	secondToken := roleToken(t, f, secondBinding)
	waitDelivered(t, p, m.MessageID, "topic", first)
	waitDelivered(t, p, m.MessageID, "topic", second)

	for i := range 8 {
		create := issueInput(f.dir, fmt.Sprintf("path-reference-%d", i))
		create.Title = fmt.Sprintf("检查发布材料 %d", i+1)
		create.Materials = []core.Material{{Label: "发布前检查", MessageID: m.MessageID}}
		token := firstToken
		if i == 0 || i == 7 {
			token = secondToken
		}
		roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
	}
	unrelated := issueInput(f.dir, "path-unrelated")
	unrelated.Title = "归档旧版本日志"
	roleWork[core.WorkReceipt](t, p, firstToken, "create_issue", unrelated)

	partial := submit(t, p, f, input("observer-path-partial-return"))
	partialIDs := []string{partial.MessageID}
	pass := core.RouteRequest{
		OperationID: "path-first-pass", MessageIDs: partialIDs, Rounds: map[string]int{partial.MessageID: 1},
		Kind: "route", Reason: "两个 Topic 各自核对", Targets: []core.RouteTarget{{TopicID: first, MessageIDs: partialIDs}, {TopicID: second, MessageIDs: partialIDs}},
	}
	dispatched := decide(t, p, router, pass)
	waitDelivered(t, p, partial.MessageID, "topic", second)
	roleCall(t, p, secondToken, "decline_message", core.DeclineMessageRequest{
		EventID: dispatched.Targets[1].EventIDs[partial.MessageID], Reason: "值班安排待澄清",
	})
	pass.OperationID, pass.Reason, pass.Rounds[partial.MessageID] = "path-second-pass", "值班安排已明确", 2
	pass.Targets = []core.RouteTarget{{TopicID: second, MessageIDs: partialIDs}}
	decide(t, p, router, pass)
	waitDelivered(t, p, partial.MessageID, "topic", second)
	// Simulate incomplete retained history for a separate Message after Core has
	// validated its Issue material. The surviving records are served by Core.
	gap := submit(t, p, f, input("observer-path-material-only"))
	gapIDs := []string{gap.MessageID}
	gapDecision := decide(t, p, router, core.RouteRequest{
		OperationID: "path-material-only", Kind: "route",
		MessageIDs: gapIDs, Rounds: map[string]int{gap.MessageID: 1}, Reason: "历史投递",
		Targets: []core.RouteTarget{{TopicID: second, MessageIDs: gapIDs}},
	})
	waitDelivered(t, p, gap.MessageID, "topic", second)
	material := issueInput(f.dir, "path-history-gap")
	material.Title = "留存材料核对"
	material.Materials = []core.Material{{MessageID: gap.MessageID}}
	roleWork[core.WorkReceipt](t, p, secondToken, "create_issue", material)
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	result, err := db.ExecContext(t.Context(), "DELETE FROM internal_events WHERE event_id=?", gapDecision.Targets[0].EventIDs[gap.MessageID])
	require.NoError(t, err)
	deleted, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	return p.endpoint
}
