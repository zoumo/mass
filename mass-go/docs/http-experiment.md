# Go HTTP v1 implementation

The authoritative [HTTP v1 contract](../../harness/bindings/http.md) defines routes,
JSON values, errors and SSE. This document describes Go assembly and CLI usage;
it does not create another protocol. The former `/experimental/operations/*` and
`/experimental/observe` endpoints are removed, with no compatibility aliases.

## Start the daemon

Choose an explicit access mode. For a trusted single-user machine:

```sh
bin/massd-go --data /absolute/owned/data --listen 127.0.0.1:24368 --local
```

For authenticated access, provide `--token-file /absolute/private/token` instead
of `--local`. A token grants access to the entire instance. Direct non-loopback
listeners also require `--tls-cert` and `--tls-key`; a trusted TLS reverse proxy can
forward to a token-protected loopback listener. The daemon does not log token
contents. Namespace grouping does not provide tenant isolation. CORS is off.

`GET /livez` and `GET /readyz` expose minimal process/admission status, not model
health. Shutdown first closes API admission and live subscriptions, then settles
Service and releases its listener and owned resources. Failure exits nonzero.

## Ordinary HTTP

Creation accepts only writable metadata and spec. The 1 MiB request body limit
returns 413 on overflow. Duplicate JSON keys, unknown fixed fields, readonly
fields, invalid/duplicate query parameters and non-JSON bodies are rejected.
Dynamic JSON uses json.Number to retain large integer precision.

```sh
curl --fail-with-body http://127.0.0.1:24368/v1/runtimes
curl --fail-with-body -H 'Content-Type: application/json' \
  -d '{"metadata":{"namespace":"demo","name":"task"},"spec":{"runtimeRef":"agent","cwd":"/agent/work"}}' \
  http://127.0.0.1:24368/v1/sessions
curl --fail-with-body -H 'Content-Type: application/json' \
  -d '{"content":[{"type":"text","text":"hello"}]}' \
  http://127.0.0.1:24368/v1/namespaces/demo/sessions/task/inputs
```

Creation returns 201 and Location; reads and action receipts return 200; Runtime
deletion returns empty 204. JSON values are direct, without result/error envelopes.
Errors are `{code,reason,message}` with contract status codes and sanitized text.
Querying history uses repeated `types` parameters and opaque after/next cursors.
ID routes and interaction path segments use canonical unpadded base64url of the
original UTF-8 reference. JSON keeps the original ID.

No request is automatically retried. A disconnected client does not cancel an
admitted operation. `outcome_unknown` is not evidence that nothing happened.
Idempotency-Key is rejected; no Operation lookup or safe-input-retry guarantee is
implemented. Existing Runtime factories still require explicit synthetic SDK
mock executable/scenario paths; real account/model execution is not implemented
by this transport migration.

## CLI and Go client

`massctl` keeps a local operation selector as command-line UX. It converts the
following stdin values into ordinary HTTP v1 methods, paths and bodies; the
operation selector and parameter object are never sent as an HTTP envelope.
`MASS_TOKEN` supplies the Bearer token for authenticated deployments.

| CLI operation | stdin fields |
|---|---|
| runtime.create | runtime: {metadata:{name},spec:{type,config}} |
| runtime.get / start / stop / delete | name |
| runtime.list | optional runtimeQuery: {type,state} |
| runtime.update | name, spec:{type,config} |
| session.create | create:{metadata:{namespace,name},spec:{runtimeRef,cwd,systemPrompt?,additionalDirs?}} |
| session.get / archive / resume / cancel | session |
| session.list | optional sessionQuery:{namespace,runtimeRef,state,after,limit} |
| session.input | session, input:{content:[{type:"text",text:"hello"}]} |
| session.answer | session, interaction, answer (the original JSON value) |
| session.history | session, optional history:{types,after,limit} |
| observe | a SessionRef directly |

SessionRef is exactly `{sessionId}` or `{namespace,name}`. Input returns
InputReceipt; answers return `{interaction,evidence}`; cancellation returns
`{result:"noActiveWork"}` or `{target,result:"controlAcknowledged"}`. Control
acknowledgment does not prove work termination. Targets are opaque work references,
not native turn IDs. Runtime updates change desired spec without restarting.

## Live observation

Use `GET {session}/events/stream` with a streaming HTTP client. Registration
precedes `subscription.ready`; subsequent business frames are `session.event`
with unchanged SessionEvent JSON. `subscription.closed` is normal observation
termination; `subscription.error` is abnormal. Heartbeat comments are emitted
at least every 15 seconds. Buffering is disabled and writes have a deadline.

The Go client ignores heartbeats, requires ready before returning Observe, and
reports EOF without an explicit terminal frame as io.ErrUnexpectedEOF. A terminal
error survives Close and future Next calls; Close releases blocked reads. It
never reconnects or follows redirects automatically, including with a supplied
http.Client. The client enforces a 16 MiB line bound with explicit error.

SSE disconnect closes only the subscription, not Agent work. Slow consumers are
bounded and fail explicitly. Last-Event-ID and resume query parameters are
rejected. Full committed facts remain queryable through history; previews and
pending interactions cannot be recovered by replay. Await ready before sending
input when its live progress matters. Receipt/event ordering across requests is
not guaranteed.
