# Go file Store implementation

This describes the current Go implementation, not a new project contract. The
current authoritative design remains under `harness/`.

The default storage root is `~/.mass`; `--data` overrides that root. There is
no extra `data/` level:

```text
~/.mass/
├── config.yaml
├── runtime.jsonl
├── session_index.jsonl
├── .store.json
├── writer.lock
├── snapshot.lock
├── sessions/
│   └── <namespace>/
│       └── <name>/
│           ├── state.json
│           └── events.jsonl
└── private/                 # Driver-owned native recovery material
```

`runtime.jsonl` contains one complete Runtime resource per line, sorted by name.
`.store.json` contains format 2 and the cursor signing key. Each Session
`state.json` contains the complete Session resource; `events.jsonl` contains one
complete persistent SessionEvent per line, in append order. Empty JSONL files
contain zero bytes. Content newlines are JSON-escaped. Every nonempty record ends
with a newline; blank lines, invalid records and unterminated final lines fail
explicitly instead of silently losing committed data.

`session_index.jsonl` is a derived directory listing, sorted by namespace/name.
Each line contains only `namespace`, `name`, `sessionId`, `runtimeRef`.
Status changes and history appends do not rewrite the index; unchanged bytes
preserve the existing file. It changes when Session membership or these locating
fields change, and is rebuilt on writable Open from validated
Session state files. A missing, stale or corrupt index cannot lose a Session;
read-only Open ignores it and never repairs it. Resource metadata is loaded into
memory; event bodies are read from the requested Session file on demand. The
disk Session index is not authoritative and is not an event-body cache.

Session paths use literal namespace/name components, not hashes or native IDs.
Empty, dot, dot-dot and separator-containing components are rejected. Store-owned
directory/file symlinks are rejected. Directory names must match stored Session
identity. Native source/run/work associations remain excluded from JSON/history.
Driver-private recovery retains its separate hashed layout under `private/`.

A writable Open owns a nonblocking OS `flock` on `writer.lock` until Close. Another
writer fails, including one in the same process. Mutations are serialized with a
mutex. A separate `snapshot.lock` is held exclusively during commits/recovery and
shared while opening or reading history, preventing visibility of partial appends.
Queries never start an Agent.

## Metadata commits

Runtime and Session resource changes atomically write/fsync `.pending.json` with
resource metadata and newly created Session identities only. It never contains
historical event payloads. Changed resource files are replaced with same-directory
mode-0600 temporary files, file fsync, rename and directory fsync. Unchanged files
retain their inodes. Runtime and Session directory JSONL files may be rewritten
as whole lists. New Sessions get an empty `events.jsonl`; existing logs are never
rewritten by metadata commits. Explicit Session deletion removes its state/event
files and directory. The metadata intent is removed only after application succeeds.
Writable reopen/Reconcile idempotently finishes an interrupted metadata commit.

## Append-only history and recovery

Append validates the incoming batch and skips equal existing event IDs. Existing
IDs are looked up in an offset index and their original event is read from disk
for exact semantic comparison; conflicting contents fail before committing any
new member of the batch. Duplicate new IDs within a batch are likewise checked.
If the entire batch already exists, no file is written and sync progress remains
at the final committed event, even when an old event is retried.

`.append.pending.json` contains only the new JSONL bytes for this batch (JSON
base64-encoded), Session address/UID/ID, original file byte offset, and a digest of
the already committed prefix. It is atomically written and fsynced before touching
the event log. The existing `events.jsonl` is opened with `O_APPEND`, written,
fsynced and closed. Only then is the intent removed and the root synced, the
in-memory offset index advanced, and success returned. Service publishes facts
only after Append succeeds. No rename, replacement or truncation of an event log
occurs during append/recovery.

Write, short-write, sync, close and intent-commit failures report outcome_unknown
and seal the Store until explicit Reconcile or writable reopen. Recovery streams
and validates the committed prefix, verifies its digest and original identity,
validates every event in the new batch, then compares any already written suffix
with the intent's exact bytes. It appends only missing bytes, including when the
interrupted suffix ends mid-record. A fully written batch is synced without
adding it again. Divergent tails, duplicate IDs, invalid intents and changed
committed prefixes fail explicitly and retain the log/intent for diagnosis;
recovery never truncates them. Read-only Open/history queries refuse a pending
commit and never repair it. An unjournaled incomplete line is corruption, not an
authorization to discard data.

## Memory and reads

Store retains resource metadata and per-event ID/type/offset/length/digest indexes,
not event bodies. Opening/reconciliation streams each log one record at a time to
validate the entire committed history and rebuild these indexes. Memory still
grows with the number of indexed events, not the total payload size. There is no
full-history array or snapshot, including in temporary commit records. Append
buffers only the incoming new batch. History queries seek/read only the indexed
records required for the requested page; progress uses the last committed ID in
the index. Content hashes detect changes to records read after Open.

A read-only Store holds the metadata/offset view captured on Open; later appends
do not change that view. Reopen to refresh. It does not retain deleted file bodies:
removing a Session may make subsequent reads through an older view fail.

There is no SQLite or automatic format migration. Legacy `state.json` at the root
(or the previous default `data/state.json`) is rejected when initializing a new
root and left untouched. Old full-history recovery snapshots are not converted.
Temporary `.write-*` files are ignored. The root is application-owned; this is
not protection against a hostile process concurrently replacing paths.

Tests cover real-file partial/short writes, fsync/close failures, in-process and
cold recovery, forced child-process exits during a partial append or after sync,
idempotent retries, cursor preservation, and corruption rejection. These tests
do not establish sudden power-loss behavior on every filesystem or recovery of
native Agent side effects.

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
implementation appends only new events through the intent protocol. History cursors are
HMAC-authenticated opaque positions bound to session and sorted type filter, using
a key persisted in `.store.json`. They survive restart and support
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
all directory/history reads come from the complete authoritative file set. Opening
a Store validates the entire committed history, including records beyond a first
page. Removing a future/redundant index cannot remove any required current data.
