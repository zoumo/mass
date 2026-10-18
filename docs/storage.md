# Native files and strict recovery

One daemon owns each data directory. Configuration uses `dataDir` and native
named runtimes from the [startup guide](../README.md). Relative dataDir resolves
to the config directory; without a config it resolves to the daemon cwd.

Files are `sessions/<private-locator>/metadata.json` and `events.jsonl`.
The fixed-size locator hashes a length-delimited encoding of stable instance identity
and the opaque Agent-returned sessionId. It is an internal path, never another Session ID.
Names and Agent IDs cannot choose filesystem paths. Different instances may return the
same ID. Metadata formatVersion=2 stores sessionName/sessionId, stable `instance`
binding (identity/type/version), opaque adapter-owned `recovery`, original spec,
historyMode, configOptions and the implemented capability snapshot. Instance and
recovery bindings are private and excluded from HTTP Session objects. Old metadata
formats are rejected and preserved; no fallback, migration or overwrite is supported.

Creation reserves the name, instance and operationId before opening. The adapter
returns the real ID before storage paths or metadata exist. The service exclusively
creates that pair's directory, writes and syncs a same-directory temporary file,
installs metadata without replacing existing files, and syncs the directory before
acknowledging success or activating Handlers. Metadata and Journal are not a cross-file
transaction. Directories without committed metadata are ignored on startup and never
silently overwritten. A failed save releases only its attachment; cleanup never stops
a healthy shared host or deletes another operation's directory. Unknown release keeps
the original reservation and its cleanup owner until actual settlement.

Each Journal line is `{"event": <schemaVersion=2 public event>, "evidence": <versioned private adapter evidence>}`.
Only the five durable [MASS fact types](events.md) are committed. Codex evidence
uses adapter=codex, version=1 and its complete original normalized/native payload.
Only the adapter interprets its content. HTTP history, SSE and Handler consumers
receive event alone; they never receive evidence. Old schemaVersion=1 lines fail
explicitly and remain untouched. No old decoder, dual write or migration exists.
One attachment writer allocates consecutive seq values, appends a newline-ended
JSON record, flushes, then publishes the same immutable object. Ordinary flush
is not fsync or a power-loss guarantee. Append failure seals that writer and
cannot broadcast its failed durable object; repairing the path alone cannot
reenable it. A crash can lose a native fact not yet committed.

History checks every committed line and consecutive seq, including records past
the requested page. An unterminated final line is uncommitted even if its JSON
parses. Read queries leave it intact and never activate a runtime or Handler.
Explicit resume first validates unchanged metadata and stable instance binding, then every public event schema, session identity and consecutive seq. Only after the complete public pass does it invoke adapter-owned binding validation, followed by private source coordinates, official payloads and exact public projections.
Only then can it truncate a partial tail and attach the same native thread.
Corrupt committed content fails without repair or native activation. The Journal
checks the two-field record envelope and opaque evidence object; it never interprets
an adapter's private version. Public pages also validate the owning metadata identity
for every committed record, including records outside the requested page.

Codex recovery bindings use adapter=codex/version=2 and retain the exact Agent ID,
reviewed program identity and a non-secret local ownership digest. The digest binds
canonical CODEX_HOME and SQLite directory paths/device/inode, directory owner,
file-backed ChatGPT account/user identity, the global config.toml digest and the canonical Session cwd. It is
captured by the owning host; token refresh with the same account/user does not change
it. Resume checks current ownership before tail repair or activation and verifies the
active host's original scope before thread/resume. Directory replacement, account/user
change or changed global configuration fails closed. Reverting the original scope is
an explicit operator action; MASS never rewrites the binding to accept a new target.

This verified recovery path requires readable file-backed ChatGPT identity with a
persistent account and user. API-key, external/ephemeral/keyring authentication,
custom model-provider selection, selected profiles, an overridden built-in provider definition, project configuration layers or missing identity evidence cannot claim verified
recovery: new sessions may operate, but explicit resume reports recovery_unavailable.
The adapter receives cwd for pre-activation validation; project/profile overrides are refused because their effective authentication source has not been independently established. No credentials, token digests or expanded environment values are persisted. The
fixed client’s non-user sources are checked before that digest is accepted.
System config and legacy managed config must be absent. System requirements may
only contain hooks and the boolean features.hooks setting; their full bytes are
hashed. Any ownership override, unknown root/feature, unreadable source, or forced
macOS config/requirements preference makes recovery unavailable. The preference
read cache is refreshed without setting or changing any preference value.

Cloud absence is established from the fixed loader’s eligibility rule and explicit
file-backed ChatGPT identity: only known personal plans (free/go/plus/pro/prolite/promax)
are accepted. Workspace, missing and unknown plan claims are refused; an empty cache
is never proof. Environment login/access-token/debug-config overrides are refused.
The user’s explicit inference URL remains bound by the complete user-config digest;
it does not change the built-in provider’s authentication-store selector. A reused
host may certify a new binding only while its original and current source proofs
match. Unproven creation bindings remain non-recoverable; resume does not replace them.

Recovery binding version 2 requires this proof. Version 1 files remain untouched
and are explicitly rejected; no old-binding decoder or migration exists. The
ownership digest is local provenance, not an authentication or filesystem-tampering
security boundary. The adapter still requires the Agent to confirm the saved ID;
a different returned ID is released and never replaces committed metadata.

Archive preserves files and native persistent context; it releases only the
attachment after actual thread release. `close_unknown` retains a closing route
and blocks resume. Same-host and cold-process resume both preserve the same Agent-returned
sessionId and next seq. Native old turns are excluded, and no user input, native
tool or Handler action is replayed. No new-session fallback exists. See
[archive/resume contract](http-api.md#archive-and-explicit-resume-ticket-05) and
final acceptance.

## Runtime registration (Runtime Ticket 01)

`runtime-registry.json` persists internal instance identities, type, configuration digest
and version. It stores neither raw configuration nor resolved environment secrets.
`runtime-registry.lock` is held until confirmed owned-resource shutdown (or daemon exit). Omission from configuration
preserves the record; referenced execution configuration cannot change. Session metadata
uses the private `instance` binding checked before activation. Missing/corrupt ledger or
mismatched bindings fail without claiming old sessions. Metadata is formatVersion=2;
events use schemaVersion=2 with versioned private evidence in the same line. Keep the registry
and sessions together when relocating data.

Remote stores the same formatVersion=2 metadata and adapter-owned Codex evidence,
with its stable instance and exact Agent-returned ID. It records no verified
service/account/data scope. Its binding is diagnostic, never a recovery credential.
`resume` is unsupported before tail repair or activation, including after restart;
offline reads still validate the entire public history. Transport termination
preserves unknown bindings and cannot synthesize terminal or release facts.
