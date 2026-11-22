# Give SQLite metadata authority its own layout

Before `massd` offered SQLite as a MetaStore authority, `metadata.sqlite` held a derived copy of resource files. A startup path that promoted that file would assume an old projection contained the full authoritative state. `massd` gives SQLite authority a separate database, `metadata-authority.sqlite`, and records its layout in `.metadata-sqlite.json`.

The marker distinguishes a new data directory from an existing authority whose database is missing. Startup fails closed in the second case so that a missing database cannot turn into a new empty authority. An existing file authority or legacy projection requires an explicit migration. EventStore continues to rebuild its own query replica from event files.

See the [original implementation decision](https://github.com/zoumo/mass/blob/6e50b9a3bfe48be2a4b0286ecf89a3403e7cc517/mass-go/docs/metadata-authority.md), the [storage contract](../../../harness/bindings/storage.md#模块与权威), and the [authority tests](../../pkg/metastore/authority_test.go).
