# HTTP v1 implementation: Runtime and Session identity (tickets 01–02)

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
Runtime operations, Session creation/listing, both Session lookup paths and probes. Creation accepts only writable metadata/spec;
PUT accepts RuntimeSpec directly. Error schemas use `{code, reason, message}`,
with the same application/json media as runtime responses. Output metadata
marks generated fields read-only. No manually maintained OpenAPI schema is used.

## Incremental migration boundary

Ticket 01 establishes the shared entry and Runtime configuration behavior.
Runtime start/stop/delete retain the existing Service lifecycle implementation;
ticket 02 supplies its remaining acceptance. Session routes are explicit chi
registrations under `/apis/v1`, preserving existing operations while removing
the string dispatcher. Raw session IDs are escaped per segment and decoded once;
full Service identity/nameOrID resolution and Session Huma models belong to
03. Session/history/SSE operations are not yet in generated OpenAPI. History
schemas (04), SSE registration/headers/real-proxy acceptance (05), generated
client and daemon/CLI completion (06–07) remain outstanding. This export is not
a claim that the full HTTP v1 surface is implemented.

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

Input/recovery/history/SSE operation schema migration and generated clients
remain assigned to tickets 03–07. Default verification uses isolated SDK mock
processes and real HTTP, not personal Agent data or model calls.
