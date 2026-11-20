package core_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type routerMessageRow struct {
	ID, State, ActivityAt, ActivityID, ActivityKind, ActivityReason, ReturnedDeliveryID string
	Targets                                                                             []struct{ ID, TopicID, Title, Reason string }
}

func routerRows(t *testing.T, p *process, query string) ([]routerMessageRow, string) {
	t.Helper()
	result := page(t, p, "router-messages"+query)
	items := make([]routerMessageRow, len(result.Items))
	for i, item := range result.Items {
		require.NoError(t, json.Unmarshal(item, &items[i]))
	}
	return items, result.NextCursor
}

// Give two real requests the same registration time in the isolated browser
// database. Source Message timestamps and every other delivery stay untouched.
func tieBrowserRouterRegistrations(t *testing.T, f *fixture) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	result, err := db.ExecContext(t.Context(), `UPDATE internal_deliveries SET created_at=(
 SELECT d.created_at FROM routing_deliveries d JOIN trigger_messages m ON m.id=d.message_id
 WHERE m.native_id='browser-53' AND d.kind='route_request'
 ) WHERE id=(SELECT json_extract(body,'$.eventId') FROM trigger_messages WHERE native_id='browser-54')`)
	require.NoError(t, err)
	count, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
}

func TestObserverRouterMessages(t *testing.T) {
	f := fixtureAt(t)
	f.execution(newRouteBoundary(t))
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	var ids []string
	for i := range 24 {
		ids = append(ids, submit(t, p, f, input("router-page-"+strconv.Itoa(i))).MessageID)
	}
	// Equal registration times exercise the stable Message ID tiebreaker.
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	const tied = "2024-01-01T00:00:00.000000000Z"
	_, err = db.ExecContext(t.Context(), "UPDATE internal_deliveries SET created_at=? WHERE kind='route_request'", tied)
	require.NoError(t, err)
	slices.Sort(ids)
	slices.Reverse(ids)
	first, next := routerRows(t, p, "")
	require.Len(t, first, 20)
	require.NotEmpty(t, next)
	for i, row := range first {
		require.Equal(t, ids[i], row.ID)
		require.Equal(t, tied, row.ActivityAt)
	}
	promoted := ids[len(ids)-1]
	newMessage := submit(t, p, f, input("after-router-cursor"))
	route := newRoute("router-promote-old", []string{promoted}, f.dir)
	route.Reason = "核对发布项目"
	decision := decide(t, p, token, route)
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	second, end := routerRows(t, p, "?cursor="+url.QueryEscape(next))
	require.Empty(t, end)
	require.Len(t, second, 4)
	for i, row := range second {
		require.Equal(t, ids[20+i], row.ID)
		require.Equal(t, tied, row.ActivityAt)
		require.Equal(t, "route_request", row.ActivityKind)
	}
	// The cursor freezes traversal, while current state and its supporting
	// targets are both read at the time the row is requested.
	require.Equal(t, "routed", second[3].State)
	require.Len(t, second[3].Targets, 1)
	require.Equal(t, route.Reason, second[3].Targets[0].Reason)
	fresh, _ := routerRows(t, p, "?limit=2")
	require.Equal(t, promoted, fresh[0].ID)
	require.Equal(t, newMessage.MessageID, fresh[1].ID)
	require.Len(t, fresh[0].Targets, 1)
	require.Equal(t, route.Reason, fresh[0].Targets[0].Reason)
	for _, path := range []string{
		"router-messages?order=oldest", "messages?order=newest&cursor=" + next,
		"router-messages?messageId=" + promoted + "&cursor=" + next, "router-messages?topicId=" + topic.OwnerID,
	} {
		status, _ := request(t, http.MethodGet, p.endpoint, "/v1/observer/"+path, "", nil)
		require.Equal(t, http.StatusBadRequest, status, path)
	}
	status, _ := request(t, http.MethodHead, p.endpoint, "/v1/observer/router-messages", "", nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = request(t, http.MethodPost, p.endpoint, "/v1/observer/router-messages", "", nil)
	require.Equal(t, http.StatusMethodNotAllowed, status)

	direct := submit(t, p, f, input("direct-after-route"))
	filter := "?messageId=" + direct.MessageID
	onlyDirect, _ := routerRows(t, p, filter)
	require.Empty(t, onlyDirect)
	topicToken := roleToken(t, f, topic)
	roleCall(t, p, topicToken, "message_return", core.ReturnRequest{DeliveryID: direct.EventID, Reason: "请检查其他目标"})
	returned, _ := routerRows(t, p, filter)
	require.Len(t, returned, 1)
	require.Equal(t, "pending", returned[0].State)
	require.Equal(t, direct.EventID, returned[0].ReturnedDeliveryID)
	require.Equal(t, "请检查其他目标", returned[0].ActivityReason)
	both := newRoute("router-both", []string{direct.MessageID}, f.dir)
	both.Rounds[direct.MessageID] = 2
	both.Targets = append(both.Targets, core.RouteTarget{TopicID: topic.OwnerID, MessageIDs: both.MessageIDs})
	both.Reason = "两个目标分别核对"
	multi := decide(t, p, token, both)
	targets, _ := routerRows(t, p, filter)
	require.Len(t, targets, 1)
	require.Len(t, targets[0].Targets, 2)
	roleCall(t, p, topicToken, "message_return", core.ReturnRequest{DeliveryID: multi.Targets[1].DeliveryIDs[direct.MessageID], Reason: "转交剩余检查"})
	partial, _ := routerRows(t, p, filter)
	require.Len(t, partial[0].Targets, 1)
	require.Equal(t, both.Reason, partial[0].Targets[0].Reason)
	require.Equal(t, multi.Targets[0].DeliveryIDs[direct.MessageID], partial[0].Targets[0].ID)
	require.Equal(t, "转交剩余检查", partial[0].ActivityReason)
	both.OperationID, both.Reason = "router-again", "只复核退回项目"
	both.Rounds[direct.MessageID] = 3
	both.Targets = both.Targets[1:]
	again := decide(t, p, token, both)
	final, _ := routerRows(t, p, filter)
	require.Len(t, final, 1)
	require.Equal(t, "routed", final[0].State)
	require.Len(t, final[0].Targets, 2)
	require.Equal(t, multi.Targets[0].DeliveryIDs[direct.MessageID], final[0].Targets[0].ID)
	require.Equal(t, "两个目标分别核对", final[0].Targets[0].Reason)
	require.Equal(t, again.Targets[0].DeliveryIDs[direct.MessageID], final[0].Targets[1].ID)
	require.Equal(t, "只复核退回项目", final[0].Targets[1].Reason)
}
