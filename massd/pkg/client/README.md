# Use the Go client

Use [`client.New`](client.go) with a `ws://` or `wss://` origin, mount base, or complete endpoint. The client fills in `/jsonrpc` for an origin or mount base. Set a positive `WriteTimeout` and `SubscriptionQueueCapacity`. `New` performs no network I/O. Call `Initialize` for an explicit handshake or let the first typed call initialize the connection.

## Observe new events before sending input

1. Call `Subscribe` and wait for it to return successfully. Each subscription owns a separate WebSocket from the one used for ordinary requests.
2. Send work on the client. Read future events with `Next`. Use `History` for committed events. Keep the original interaction ID from live events when you call `Answer`. Passing `nil` as the answer sends a JSON `null`.
3. Call `Subscription.Close` when you finish observing. Call `Client.Close` when you finish using the client. It closes all connections owned by that client.

An explicit clean `subscription.closed` notification yields `io.EOF`. An unexpected disconnect or queue overflow yields an error. Reconnect explicitly and query history if you need events from the gap. A connection failure or expired request context ends your wait. Reconcile an unknown side effect before repeating the call. The [WebSocket contract](../../../harness/bindings/websocket.md#连接与订阅隔离) defines isolation and closure behavior.

## Load retained history

`LoadSession` returns a `loadId` and an accepted Session. Poll `GetSession` for `historyLoad`. Wait for `historyLoad.completed=true` and `historyLoad.deliveryCompleted=true` before sending input on the retained connection. Use `History` separately and pass its opaque cursor to continue. Pass the original `loadId` to `CancelHistoryLoad` for an unfinished load. A successful load can retain its connection with `cleanupCompleted=false`. Replayed interactions carry no live answer authority. See the [load contract](../../../harness/modules/service/load.md) for all completion and cleanup fields.

## Change live Session configuration

Use `SetSessionConfig` to send one `session.setConfig` request to the current online main Session. Wait for each call before sending the next change or input when order matters. The receipt describes acceptance evidence for that request. Initial values saved in the Session spec are applied on resume. See the [configuration contract](../../../harness/modules/runtime/session-config.md) and the [Go method](websocket_session.go) for request, receipt, and error handling.
