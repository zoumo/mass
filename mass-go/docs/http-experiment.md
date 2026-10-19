# Experimental Go HTTP + SSE mapping

This is a Go implementation choice. `harness/bindings/http.md` deliberately leaves
URLs/envelopes unspecified; this document does not finalize or modify harness.
The server calls only Service interfaces. It never imports native Session catalogs,
stores transport history or projects another event representation.

## Operations

`POST /experimental/operations/{operation}` accepts one JSON object (maximum
1 MiB). JSON numbers use `UseNumber`; unknown fields, trailing JSON and non-object
bodies are rejected. The fields below select the corresponding Service operation.
SessionRef is exactly `{"sessionId":"native-id"}` or
`{"namespace":"demo","name":"session"}`. `session` below means that object.

| operation | request fields |
|---|---|
| runtime.create | `runtime: {metadata:{name},spec:{type,config}}` |
| runtime.get / start / stop / delete | `name` |
| runtime.list | optional `runtimeQuery: {type,state}` |
| runtime.update | `name`, `spec:{type,config}` (full desired replacement) |
| session.create | `create:{metadata:{namespace,name},spec:{runtimeRef,cwd,systemPrompt?,additionalDirs?}}` |
| session.get / archive / resume / cancel | `session` |
| session.list | optional `sessionQuery:{namespace,runtimeRef,state,sessionId,after,limit}` |
| session.input | `session`, `input:{content:[{type:"text",text:"hello"}]}` |
| session.answer | `session`, `interaction`, `answer` (native permission/form shape) |
| session.history | `session`, optional `history:{types,after,limit}` |

Example: `{"name":"agent"}` starts a registered Runtime using `runtime.start`.
Creation never implicitly starts a Runtime or submits input; reads never activate
or resume anything. `archive` is the public close/release operation and retains
identity/history. There is no Session update/delete or JSON Patch.

Success is HTTP 200 with `{"result": SERVICE_VALUE}` (delete result is null).
Failure is `{"error":{"code","reason","message"}}` with stable public text.
Known classifications originate at rejection sites/sentinels, not parsed native
error strings: invalid_argument=400, not_found=404, conflict=409,
failed_precondition=409, unavailable=503. Unclassified native/Store/I/O failures
are conservatively outcome_unknown=500; inspect authoritative state and original
ownership, never infer no effect. Some internal diagnostics do not yet have a
finer public classification. No operation or unknown result is automatically
retried. Default client redirect following is disabled to prevent POST replay.
Native/path/dynamic diagnostics never become public error messages. Runtime
configuration currently accepts only explicit absolute mock executable/scenario
paths; it does not accept or inherit account secrets.

Service values retain their business meaning: input/answer/cancel receipts show
only available sent/nativeAccepted/nativeRecorded evidence, not completed work.
Desired configuration differs from effective generation until stop/start.
Session/history cursors are opaque, exclusive and filter-bound; UUID/time are
never pagination positions. Full facts retain exactly the same event identity,
time and payload through Store, SSE and history. There is no seq or event projector.

## Observation

`POST /experimental/observe` accepts the SessionRef directly. On a pre-registration
error it returns the ordinary JSON error. Otherwise it sets `text/event-stream`
and sends frames with one `event:` and one JSON `data:` line, separated by a blank
line. The request context owns only this independent Service subscription.

| event | data and meaning |
|---|---|
| ready | `{}`; Service subscription has been registered before this frame |
| session | the unchanged SessionEvent; only events published after registration |
| end | `{}`; Service subscription ended normally |
| error | stable `observation_failed/subscription_terminated` error; delivery incomplete |

Registration precedes ready; events between registration and ready remain in the
bounded Service queue. After ready returns, callers may safely submit input.
No history is backfilled; there is no Last-Event-ID, reconnect or cross-connection
resume. Subscribe after completion and you receive only future events. Historical
reads are a separate explicit operation.

Only `end` produces normal remote EOF. Socket EOF without an end frame is
`io.ErrUnexpectedEOF`; a server error frame remains an error on later Next calls
and Close. The client enforces a 16 MiB frame bound with explicit error rather than
silent truncation. Next is single-consumer; Close may run concurrently and releases
blocked reads. Explicit local Close ends observation without an Agent side effect.

Server-side overflow or subscription failure emits an error if the connection
can still write. If the socket cannot carry it, abnormal disconnection still
cannot be interpreted as complete delivery. Client cancellation closes only its
subscription. Daemon signals cancel HTTP waits, close streams and settle Service
with a bounded deadline; an interrupted connection may end abnormally and makes
no promise of history completion. Failed Service shutdown exits nonzero.
There is no readiness endpoint, remote authentication/authorization or production
HTTP compatibility promise in this experiment.
