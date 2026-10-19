# Initial Go file Store implementation

This describes the Ticket02 implementation, not a new project contract. The
current authoritative design remains under `harness/`.

`state.json` contains a format marker, cursor signing key, complete Runtime and
Session resources, and ordered arrays of the same SessionEvent values used live.
The format marker belongs to the file, never to individual events. Native Driver
source/run/work associations are excluded from JSON and history.

A writable Open owns a nonblocking OS `flock` on `writer.lock` until Close. Another
writer fails, including one in the same process. One Store serializes mutations
with a mutex. A read-only Open reads one committed snapshot without taking writer
ownership. To refresh that snapshot, reopen it. Queries never start an Agent.

Each mutation copies the committed snapshot, checks uniqueness/version conditions,
encodes the entire candidate, writes a mode-0600 temporary file in the same data
directory, fsyncs and closes it, atomically renames it over `state.json`, and fsyncs
the directory. Only then is success returned and the in-memory snapshot installed.
Service publishes persistent facts only after Append succeeds. A failure after
rename is reported as outcome_unknown and seals that Store; reopen is required to
reconcile file authority. Failure does not authorize generating a new event UUID.
Temporary files are never authoritative and are ignored on open.

There is no SQLite. File decoding fails on unsupported formats, trailing data,
unknown structural fields, invalid resource identities, duplicate IDs, orphan
history, invalid event branches or ownership. It does not silently discard a bad
record, repair a tail, or regenerate metadata. A complete snapshot is suited to
this first vertical path; it rewrites history and is not a scale claim. Process
restart and readback are tested; sudden power failure, disk-full/rename/fsync fault
injection and crash recovery of native side effects are not yet accepted.

Store allocates UID, creation time, generation=1 and opaque resourceVersion on
create. Update compares the expected version and immutable metadata atomically.
Canonical JSON equality decides spec changes; changed specs increment generation,
status-only changes do not. Fully equal updates retain their version. Returned and
stored values are detached snapshots. Native sessionId is immutable and globally
unique, separately from the session namespace/name key.

Dynamic configuration, tool values and event extensions use an explicit initial
JSON encoding. Supported values are null, booleans, UTF-8 strings, Go numeric
scalars/json.Number, []any and map[string]any recursively. Unsupported Go types,
non-finite numbers and excessive nesting/cycles fail explicitly; arbitrary structs,
custom marshalers and []byte are rejected instead of silently coercing or omitting
fields. Decoding uses json.Number to preserve large numeric values. This encoding
does not promise to preserve Go concrete numeric types. Tool rawOutput distinguishes
absent, explicit null and empty string. Unsupported content/event branches fail
rather than being silently deleted; this slice implements text content and message,
thinking, chunk, command-tool and observed-state event branches.

Append accepts only the five persistent event types and validates session/name
ownership. It preserves eventId/time and append order. Equal UUID retries do not
move events or syncedEventId; conflicting content for the same UUID fails. This
implementation commits a batch in one full-file replacement. History cursors are
HMAC-authenticated opaque positions bound to session and sorted type filter, using
a key persisted in the authoritative snapshot. They survive restart and support
limit changes; they are independent of UUIDs and timestamps. SyncedEventId always
reflects the entire committed history, even for a filtered page.

The Service uses bounded Driver, creation, session and subscriber queues. Each
session has one persistence worker; the Driver receive loop performs no Store I/O.
Overflow is an explicit failure; broader isolation, pending-fact settlement and
stop/failure guarantees remain Ticket05 acceptance work. Normal Shutdown drains the
stream and workers and releases the owned SDK child; it is not a substitute for
fault, late-result and unknown-operation settlement in Ticket05.


Ticket04 adds complete resource directory queries and conditional deletion to
MetaStore. Session cursors are separately tagged/signed, bound to all filters,
and store namespace/name/uid exclusive positions, never UUID ordering. History
cursors cannot serve as resource cursors or vice versa. No SQLite index is used;
all directory/history reads come from the complete authoritative snapshot. Opening
a Store validates the entire committed history, including records beyond a first
page. Removing a future/redundant index cannot remove any required current data.
