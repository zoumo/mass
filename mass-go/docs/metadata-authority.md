# Metadata authority

`massd` reads the selected authority from its normal Viper YAML configuration:

```yaml
data: /absolute/owned/data
listen: 127.0.0.1:24368
local: true
metaStore:
  backend: sqlite # default when omitted; file is the other supported value
```

Both Runtime and Session use that single backend. Unknown values, invalid field types, corrupt storage and unavailable selected storage fail startup. There is no fallback, dual write or implicit import. CLI and HTTP resource operations use the selected store through Service.

SQLite stores full resources in `metadata-authority.sqlite`, with `.metadata-sqlite.json` identifying the authority layout. Preserve both as authoritative data; stop the writer before making a consistent backup. Neither file is a cache. A missing database with an existing marker fails closed; restore a verified authoritative backup. An interrupted first initialization can leave a marker or incomplete database and also fails closed, requiring explicit operator verification rather than automatic deletion. No resource JSONL/state copies are written in this mode.

The file backend uses `.store.json`, `runtime.jsonl`, `session_index.jsonl`, Session `state.json` files and resource-only `.pending.json` redo evidence. It does not create, open, delete or rebuild `metadata.sqlite`; any legacy projection is left untouched. Its existing commit/recovery protocol and directory layout remain in use.

Both backends retain the sole-writer `writer.lock` ownership rule. SQLite resource comparison, version/generation allocation and changed-row persistence occur in one transaction. SQL reads take a consistent transaction snapshot. The shared resource engine preserves CRUD, uniqueness, snapshot isolation, filtering and authenticated pagination across both backends.

Changing `metaStore.backend` is not migration. Opening SQLite over existing metadata files or a legacy `metadata.sqlite` projection fails with an explicit migration requirement; the old derived database is never promoted. Selecting file over an existing SQLite authority also fails. Use a distinct, explicitly chosen data root for new empty storage, or perform a separately designed and verified migration. This ticket provides no migration tool and never moves personal data.

EventStore remains independently owned: its `history.sqlite` is a disposable event query replica, rebuilt from event files and identity/recovery materials. Rebuilding it must not delete or rebuild metadata authority. MetaStore can open, operate and close without EventStore, and vice versa. This does not provide a cross-store transaction or decide Session deletion/retention policy.

Verification uses real temporary file/SQLite stores and synthetic Service/HTTP/CLI entrypoints; it does not establish real Agent/model behavior. Broadcast/async status-handler changes belong to later tickets.
