# HTTP v1 implementation: Runtime, Session identity, work and recovery (tickets 01–05)

The contract is [harness/bindings/http.md](../../harness/bindings/http.md).
This note records implementation coverage, not a replacement contract.

Runtime configuration uses Huma v2 with chi, at `/apis/v1/runtimes`:
POST creates a stopped resource, GET reads/lists, and PUT on the original
resource path replaces the complete expected spec. Configuration validation,
read-only business fields, metadata constraints and state filtering belong to
Service. Store owns revisions, snapshots and atomic conditional writes. Config
access never constructs a Driver; running configuration updates retain the
existing Driver and applied generation.

The common HTTP entry rejects unknown/duplicate query parameters, unknown fixed
JSON fields, read-only writes, duplicate JSON keys and invalid media. JSON numbers
remain `json.Number` through request binding and persistence. Huma request-size
and body-read timeout defaults are explicitly disabled, per ADR-0001. Probes
report HTTP process/admission health, not Agent or model readiness. `/v1` has no
compatibility routing.

From `mass-go/`, export without opening a Store, reading daemon configuration,
listening or starting an Agent:

```sh
rtk proxy go run ./cmd/massd openapi > /tmp/mass-openapi.json
```

The exporter runs exactly the server's type registration. Current OpenAPI covers
Runtime operations, Session creation/listing, both Session lookup paths, both sets of work and recovery operations and probes. Creation accepts only writable metadata/spec;
PUT accepts RuntimeSpec directly. Error schemas use `{code, reason, message}`,
with the same application/json media as runtime responses. Output metadata
marks generated fields read-only. No manually maintained OpenAPI schema is used.

## Incremental migration boundary

Tickets 01–05 register Runtime resources and lifecycle entry points, Session
creation/directory/lookup, and input/answer/cancel/archive/resume operations with Huma.
History and SSE retain explicit chi registrations pending tickets 04–06; generated
client completion belongs to 07. This export does not yet cover the full HTTP v1
surface. Existing recovery and event behavior remains under regression tests.

Deterministic tests use public Service methods and real HTTP with owned temporary
Store directories; the fixture rejects Driver construction on config-only paths.
Existing synthetic lifecycle tests additionally verify concurrent updates versus
applied generation. These results do not prove native Agent/model behavior.


Session identity and directory (ticket 02) use the same Huma registration.
Creation waits for native open and persistent identity, returns 201 with a global
raw-ID Location escaped as one path segment, and sends no input. Service rejects
UUID-shaped creation names before runtime access. Complete `.` and `..` IDs are
invalid at Service entry, native open/resume and persistence boundaries. Invalid
native identity cleanup stays with its original operation; unknown cleanup keeps
its reservation and returns outcome_unknown.

Service.ResolveSession(namespace, nameOrID) checks exact name and global ID
candidates, constrains ID matches to the namespace, rejects ambiguity, preserves
directory failures and returns an exclusive fixed-ID SessionRef. Both typed GET
routes and remaining explicit Session action routes use this resolver. The
original mutually exclusive SessionRef forms remain available in process.
Session queries validate filters and include offline/archived persistent records
without starting a Runtime. Typed and explicit paths preserve encoded segment
boundaries and decode once; no base64 Session ID alias is provided.

History/SSE operation schema migration and generated clients
remain assigned to tickets 06–07. Default verification uses isolated SDK mock
processes and real HTTP, not personal Agent data or model calls.


Session work (ticket 03) resolves a name once to a fixed ID, pins the original
Driver/lane under the Service admission gate, and retains that gate until native
dispatch settles. Caller cancellation abandons only its wait and returns
outcome_unknown; no automatic retry or compensating cancel is sent. Cancellation
remains available during drain and its receipt does not update Session state.
Native work and interaction identity checks remain with the original Driver.

Service validates public content and JSON answers before dispatch. Content is
snapshotted through its union codec; answers retain json.Number and may be any
JSON value, including null. HTTP requires an answer body, so literal null differs
from missing input. Both URL sets use typed Huma request/receipt models; the
transport schema registry describes ContentBlock's five private-codec branches,
including the mutually exclusive embedded text/blob resources. The domain module
has no Huma/chi dependency. Acceptance evidence retains absent, false and true.

New fault fixtures verify Service ownership and real HTTP disconnect/SSE/history
continuation. Protocol fixtures cover authentication, Idempotency-Key rejection,
strict bodies, canonical interaction base64url, content unions and open JSON
precision. These are deterministic contract checks; they are not real Agent/model
or proxy acceptance. No massctl answer command is implemented.

Session recovery (ticket 04) uses per-Session lifecycle coordination and dispatch
admission. An unrelated Session can archive/resume while another close waits.
Archive seals new inputs/answers, waits for admitted input/answer/cancel and policy
responses, confirms Driver close and commits queued facts before clearing applied
versions. Unknown close retains the original lane and prevents restore. Caller
cancellation ends only the wait, with outcome_unknown; the Service owner continues.

Restore validates the complete committed public history before Driver dispatch;
the Driver validates its private recovery material. Opening is observable; a
confirmed resource-free failure restores the prior status. Uncertain restore or
status publication retains the original reservation for explicit settlement.
Unexpected native identities are released under that reservation without replacing
the saved identity. Identity, creation time, spec, history UUIDs and sync position
remain unchanged by successful restore, and no business input is replayed.

One persistent delivery queue survives archive/restore. Old source bindings remain
associated with that queue until the original Driver stream ends, so late facts
retain append order and original identity. Only the active source can update
current resource state or dispatch a policy response. Runtime stop drains and
releases these retained delivery owners; they do not represent native connections.
The four recovery URLs are typed and generated from the same registration as the
server. Deterministic Service/HTTP and SDK mock checks do not prove real Agent/model
behavior or final generated-client/production-proxy acceptance.


Runtime stop (ticket 05) belongs to Service for both in-process and HTTP callers.
It atomically seals admission and shares the original stop attempt between callers;
request cancellation returns outcome_unknown and ends only that caller's wait.
Original opening/restore and input/answer/cancel/policy dispatches settle before
Session release. Release seals cancel admission too. Failed release retains stopping
and the original Driver; explicit settlement does not resend confirmed Session or
Driver releases. Final stream routing is checked again after Driver stop, including
retained overflow on archived or recovered sources. Delivery settlement preserves
the original queue, event identity and append order.

Successful stop leaves Sessions offline and preserves the Runtime configuration,
Session identity, history and subscribers. Explicit restart captures the latest
configuration generation. Delete requires no live/unsettled binding, no persistent
Session reference (including archives), and confirmed stopped status; it never
cascades or implicitly stops. Typed HTTP registration returns 204 with no body for
successful deletion. The existing generated lifecycle schema remains applicable.
Deterministic Service/real HTTP and SDK mock verification does not prove native
Agent/model or deployment-proxy behavior.
