package core_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const heartbeatPath = "/v1/channel/heartbeat"

type healthReceipt struct {
	Status     string
	HealthID   string
	AcceptedAt string
	ExpiresAt  string
}

func healthInput(instance string) map[string]any {
	return map[string]any{
		"source": "dws", "accountId": "account-1", "instanceId": instance,
		"operationId": "health-1", "connection": "connecting", "consumerReady": false,
		"readyConsumers": 0, "consumers": 1, "backlog": 0, "capacity": 100, "admission": "waiting",
	}
}

func sendHealth(t *testing.T, p *process, token string, payload map[string]any) healthReceipt {
	t.Helper()
	status, raw := request(t, http.MethodPost, p.endpoint, heartbeatPath, token, payload)
	var receipt healthReceipt
	if status != http.StatusOK || json.Unmarshal(raw, &receipt) != nil || receipt.Status != "accepted" ||
		receipt.HealthID == "" || receipt.AcceptedAt == "" || receipt.ExpiresAt == "" {
		t.Fatalf("bad heartbeat response %d: %s", status, raw)
	}
	return receipt
}

func readHealth(t *testing.T, p *process, id string) map[string]any {
	t.Helper()
	status, raw := request(t, http.MethodGet, p.endpoint, "/v1/observer/channel-health/"+id, "", nil)
	var health map[string]any
	if status != http.StatusOK || json.Unmarshal(raw, &health) != nil {
		t.Fatalf("bad health details %d: %s", status, raw)
	}
	return health
}

func TestPublicChannelHeartbeat(t *testing.T) {
	f := fixtureAt(t)
	otherToken, remoteToken, unboundToken := strings.Repeat("b", 48), strings.Repeat("c", 48), strings.Repeat("d", 48)
	for name, token := range map[string]string{"other": otherToken, "remote": remoteToken, "unbound": unboundToken} {
		write(t, filepath.Join(f.dir, name+".token"), token)
	}
	write(t, f.config, "dataDir: ./data\nlisten: 127.0.0.1:0\nsources:\n"+
		"  - {source: dws, accountId: account-1, instanceId: collector-1, tokenFile: ./source.token}\n"+
		"  - {source: dws, accountId: account-1, instanceId: collector-2, tokenFile: ./other.token}\n"+
		"  - {source: dws, accountId: account-1, tokenFile: ./unbound.token}\n"+
		"  - {source: remote, accountId: account-2, instanceId: collector-1, tokenFile: ./remote.token}\n")
	p := f.start()
	first := submit(t, p, f, input("before-heartbeat"))
	health := healthInput("collector-1")
	status, raw := request(t, http.MethodPost, p.endpoint, heartbeatPath, "", health)
	errorCode(t, status, raw, http.StatusUnauthorized, "unauthorized")
	for _, credential := range []string{otherToken, remoteToken, unboundToken} {
		status, raw = request(t, http.MethodPost, p.endpoint, heartbeatPath, credential, health)
		errorCode(t, status, raw, http.StatusForbidden, "forbidden")
	}
	for _, field := range []string{"source", "accountId"} {
		bad := healthInput("collector-1")
		bad[field] = "forged"
		status, raw = request(t, http.MethodPost, p.endpoint, heartbeatPath, f.token, bad)
		errorCode(t, status, raw, http.StatusForbidden, "forbidden")
	}
	for field, value := range map[string]any{
		"instanceId": "invalid id", "operationId": "", "connection": "running", "consumerReady": true,
		"backlog": 101, "capacity": 0, "lastReceivedAt": "yesterday", "lastError": "raw log line",
		"stopReason": "buffer_full", "authorization": f.token, "text": "input body",
	} {
		bad := healthInput("collector-1")
		bad[field] = value
		status, raw = request(t, http.MethodPost, p.endpoint, heartbeatPath, f.token, bad)
		errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
		if bytes.Contains(raw, []byte(f.token)) {
			t.Fatal("credential appeared in a validation error")
		}
	}
	if len(page(t, p, "channel-health").Items) != 0 {
		t.Fatal("rejected heartbeats changed health")
	}
	connecting := sendHealth(t, p, f.token, health)
	if got := readHealth(t, p, connecting.HealthID); got["status"] != "connecting" || got["stale"] != false {
		t.Fatalf("connecting process appeared ready: %+v", got)
	}
	other := sendHealth(t, p, otherToken, healthInput("collector-2"))
	if other.HealthID == connecting.HealthID {
		t.Fatal("sibling used the same record")
	}
	if repeated := sendHealth(t, p, f.token, health); repeated != connecting {
		t.Fatal("replay changed receipt")
	}
	health["lastReceivedAt"] = "2026-11-18T09:00:00+08:00"
	status, raw = request(t, http.MethodPost, p.endpoint, heartbeatPath, f.token, health)
	errorCode(t, status, raw, http.StatusConflict, "conflict")
	health["operationId"], health["connection"], health["consumerReady"], health["readyConsumers"] = "health-2", "ready", true, 1
	ready := sendHealth(t, p, f.token, health)
	health["lastReceivedAt"] = "2026-11-18T01:00:00Z"
	if replay := sendHealth(t, p, f.token, health); replay != ready || ready.HealthID != connecting.HealthID {
		t.Fatal("equivalent timestamp replay or stable identity did not hold")
	}
	if got := readHealth(t, p, ready.HealthID); got["status"] != "ready" || got["lastReceivedAt"] != "2026-11-18T01:00:00Z" {
		t.Fatalf("ready state or last received time missing: %+v", got)
	}
	health["operationId"], health["admission"], health["backlog"] = "health-3", "failed", 1
	health["lastErrorAt"], health["lastError"] = "2026-11-18T01:00:10Z", "unavailable"
	failed := sendHealth(t, p, f.token, health)
	if got := readHealth(t, p, failed.HealthID); got["status"] != "admission_failed" || got["connection"] != "ready" {
		t.Fatalf("admission failure was hidden: %+v", got)
	}
	health["operationId"], health["connection"], health["consumerReady"], health["readyConsumers"] = "health-4", "disconnected", false, 0
	disconnected := sendHealth(t, p, f.token, health)
	if got := readHealth(t, p, disconnected.HealthID); got["connection"] != "disconnected" {
		t.Fatalf("disconnect was lost: %+v", got)
	}
	health["operationId"], health["backlog"], health["stopReason"] = "health-5", 100, "buffer_full"
	full := sendHealth(t, p, f.token, health)
	if old := sendHealth(t, p, f.token, healthInput("collector-1")); old != connecting {
		t.Fatal("old operation changed its receipt")
	}
	p.stop(t, true)
	p = f.start()
	if got := readHealth(t, p, full.HealthID); got["status"] != "buffer_full" || got["backlog"] != float64(100) ||
		got["lastError"] != "unavailable" || got["acceptedAt"] != full.AcceptedAt {
		t.Fatalf("latest health did not survive restart: %+v", got)
	}
	if replay := sendHealth(t, p, f.token, health); replay != full {
		t.Fatal("operation receipt did not survive restart")
	}
	if len(page(t, p, "channel-health").Items) != 2 {
		t.Fatal("unexpected number of health records")
	}
	cmd := exec.CommandContext(t.Context(), build(t), "observe", "channel-health", full.HealthID, "--endpoint", p.endpoint)
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	if output, err := cmd.CombinedOutput(); err != nil || !bytes.Contains(output, []byte(full.HealthID)) {
		t.Fatalf("read-only CLI: %v %s", err, output)
	}
	status, _ = request(t, http.MethodPost, p.endpoint, "/v1/observer/channel-health", f.token, health)
	if status != http.StatusMethodNotAllowed {
		t.Fatal("Observer accepted write")
	}
	status, raw = request(t, http.MethodHead, p.endpoint, "/v1/observer/channel-health/"+full.HealthID, "", nil)
	if status != http.StatusOK || len(raw) != 0 {
		t.Fatal("health detail HEAD did not match GET status")
	}
	events := page(t, p, "events").Items
	if len(events) != 1 || !bytes.Contains(events[0], []byte(first.EventID)) || !bytes.Contains(events[0], []byte(`"intent":"act"`)) {
		t.Fatalf("health changed existing routing events: %s", events)
	}
	second := submit(t, p, f, input("after-heartbeat"))
	if second.EventID == first.EventID || len(page(t, p, "events").Items) != 2 {
		t.Fatal("message ingress did not produce its normal routing event after heartbeat")
	}
}

func TestPublicChannelHeartbeatExpires(t *testing.T) {
	t.Parallel()
	f := fixtureAt(t)
	write(t, f.config, "dataDir: ./data\nlisten: 127.0.0.1:0\nsources:\n"+
		"  - {source: dws, accountId: account-1, instanceId: collector-1, tokenFile: ./source.token}\n")
	p := f.start()
	health := healthInput("collector-1")
	health["connection"], health["consumerReady"], health["readyConsumers"] = "ready", true, 1
	receipt := sendHealth(t, p, f.token, health)
	if got := readHealth(t, p, receipt.HealthID); got["status"] != "ready" {
		t.Fatalf("fresh heartbeat: %+v", got)
	}
	accepted, err := time.Parse(time.RFC3339Nano, receipt.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, receipt.ExpiresAt)
	if err != nil || expires.Sub(accepted) != 30*time.Second {
		t.Fatalf("bad Core freshness timestamps: %+v", receipt)
	}
	// Wait for the actual production freshness window, including on a restarted Core.
	p.stop(t, true)
	time.Sleep(time.Until(expires) + 100*time.Millisecond)
	p = f.start()
	if replay := sendHealth(t, p, f.token, health); replay != receipt {
		t.Fatal("replay refreshed the old operation")
	}
	if got := readHealth(t, p, receipt.HealthID); got["status"] != "stale" || got["stale"] != true || got["connection"] != "ready" {
		t.Fatalf("expired ready report appeared current: %+v", got)
	}
	health["operationId"] = "fresh-operation"
	sendHealth(t, p, f.token, health)
	if got := readHealth(t, p, receipt.HealthID); got["status"] != "ready" || got["stale"] != false {
		t.Fatalf("new report did not refresh the persisted snapshot: %+v", got)
	}
}
