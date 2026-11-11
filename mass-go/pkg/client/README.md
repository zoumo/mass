# Generated HTTP client

`client.Client` is the MASS facade used by massctl. Resource methods use the
[OpenAPI-generated types and request builders](generated/client.gen.go),
then execute requests directly through a shared Resty client. Pass raw Session
IDs in SessionRef. Paths are escaped once by the generated request builder; complete `.` and `..`
segments and malformed references are rejected before constructing requests.

Regenerate from the current registered server operations, without starting a
listener, loading a Store or starting an Agent:

```sh
cd mass-go
rtk proxy go generate ./pkg/client
```

The build-ignored `generate.go` exports the exact Huma OpenAPI 3.1 contract to
`generated/openapi.json`, then applies Go-only hints to a temporary generation
input. [oapi-codegen v2.8.0](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.8.0)
is pinned in that command; its configuration is `oapi-codegen.yaml`. Generated
files must not be edited. No schema downgrade or alternate SDK is maintained.

Open JSON and unions use json.RawMessage, including optional explicit null.
The facade builds generated writable request models and decodes responses with
UseNumber, preserving open JSON integers such as 9007199254740993. Events decode
into the existing events.SessionEvent, not a second event representation.
`Answer(ctx, ref, interaction, nil, out)` sends literal JSON null. CLI answer
parameters remain deferred; there is no answer command in this release.

`client.New(url, token)` initializes one [Resty v3.0.0-rc.4](https://github.com/go-resty/resty/releases/tag/v3.0.0-rc.4)
client, shared by ordinary requests and SSE. It configures Bearer authentication,
disables retries and redirects, and preserves explicit request body lengths.
`Client.HTTP` is the `*resty.Client` from `resty.dev/v3`; set its transport and
timeout before use. Call `Client.Close()` after all requests and observers end;
the CLI closes its client on every exit path.
Do not enable retries, redirects or automatic response parsing. Request contexts
control deadlines by default. Resty v3 timeouts also use context; a client-wide
timeout limits an SSE stream's entire lifetime. The facade closes ordinary
response bodies. A failed wait does not cancel Agent work.
Requests carry the generated Content-Length through the request-level
`SetContentLength(int64)` API. Resty response parsing is disabled with
`SetResponseDoNotParse(true)` so the facade retains JSON number precision
and reads SSE incrementally. Both paths read and close Resty's wrapped
`Response.Body`, which retains timeout cleanup and content decompression.
Observe returns only after subscription.ready; Next distinguishes closed, error
and unexpected EOF. Observer.Close releases just this observation. There is no
automatic reconnect or resume, and no arbitrary frame-size limit.

Client/HTTP/CLI and fixed SDK mock evidence verifies protocol and deterministic
workflows. It does not establish real Agent/model or deployed-proxy acceptance.

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
