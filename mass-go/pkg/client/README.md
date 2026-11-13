# WebSocket JSON-RPC client

`client.Client` is the public MASS service client used by `massctl` and product
tests. The public constructor is:

```go
func New(serverURL, token string, opts Options) (*Client, error)

type Options struct {
	WriteTimeout              time.Duration
	SubscriptionQueueCapacity int
}
```

Use an explicit `ws://` or `wss://` endpoint ending in `/rpc`. Construction
performs no network I/O; the URL is validated when a connection is first
needed. `WriteTimeout` bounds each WebSocket write and is a Go
`time.Duration`; `SubscriptionQueueCapacity` is a count of locally queued
SessionEvents per observation. Neither option has an implementation default:
zero, negative values, or an omitted field makes `client.New` fail before a
connection exists. Queue overflow terminates only that observation and
releases its connection.

`client.Client` and `client.New` remain the only public client names. There is
no `WebSocketClient`, `NewWebSocket` compatibility alias, protocol
auto-detection or HTTP fallback. `Initialize` exposes the side-effect-free
handshake, while typed Runtime and Session methods initialize lazily on first
use.

Ordinary calls share one JSON-RPC connection. `Subscribe` always opens a separate
authenticated and initialized WebSocket, sends `session.subscribe`, and returns
only after the successful response. Each connection has one active subscription.
Events are delivered in order through `session.event`; the terminal
`subscription.closed` notification becomes `io.EOF` for a clean close or a typed
`runtime.OperationError` for an error. `Subscription.Close` releases only that
observation. There is no automatic reconnect, replay or subscription resume.

Service `OperationError` values retain `code/reason/message` through JSON-RPC
error code `-32000`. Unknown outcomes remain `outcome_unknown`; callers must not
retry side effects merely because a connection timed out or closed. Request
contexts bound only that wait and never cancel admitted Agent work.

Open JSON and unions use `json.RawMessage`, including optional explicit null.
Strict JSON decoding preserves integer lexemes for open values such as
9007199254740993. Events decode into the existing `events.SessionEvent`, not a
second event representation. `Answer(ctx, ref, interaction, nil, out)` sends
literal JSON null. There is no CLI answer command in this release.

Call `Client.Close()` after all calls and observations end; the CLI closes its
client on every exit path. Client, CLI and fixed SDK mock evidence verify the
protocol and deterministic workflows. It does not establish real Agent/model,
TLS proxy or deployed-network acceptance.

## Asynchronous history loading

`LoadSession` returns a loadId and accepted Session, not completed history. Query
`GetSession` for historyLoad nativeVerified/nativeResumed/nativeCompleted and
completed/deliveryCompleted; query `History` separately with its opaque cursor.
`CancelHistoryLoad` requires the original loadId. A timeout ends only this request;
it neither cancels background work nor triggers an automatic retry. A stale cancel
cannot close the retained connection or later input after completed load.
Cleanup evidence is separate: unknown release remains unsettled, while successful
load intentionally retains its connection. Replay never grants old interaction
authority. Use `Answer` only for a current interaction observed from live events.
See [current layered acceptance](../../docs/acceptance.md) for actual test scope.

## Live main Session configuration

`SetSessionConfig(ctx, ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "native-model"}, out)`
sends one transient model or effort change through `POST {session}/config`. Runtime aliases the SDK's
Go change/receipt types; generated DTOs carry camelCase wire fields. A `runtime.ConfigReceipt`
can receive successful responses. HTTP failures retain the three-field OperationError and do not
populate a failure receipt. No retry occurs, including after outcome_unknown.

Values are nonempty UTF-8 literals without NUL. The Session must already have a valid online binding;
the call does not start, resume, wait for idle, or persist configuration. Serialize calls when order
matters. An acceptance receipt does not prove the value is effective for the current turn.
