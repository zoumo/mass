# Current Codex client implementation

See the [current API](../http-api.md) and [adapter behavior](../design/native-runtime-connectors.md).

## Official Rust client and transport

Ticket 04 directly links official codex-app-server-client and codex-app-server-protocol at fixed git revision d27764b82f7118f674371e6d6e76271d9d606edb. Runtime owns a shared fixed app-server child and a private Unix socket directory; transport is app_server_unix_websocket, connectorVersion=2. SDK owns handshake, frames, request routing and server-request resolution. MASS owns admission, generations, policy and durable facts. The fixed client source is retained under vendor with LICENSE/NOTICE, pristine hashes,
UPSTREAM.json and reversible MASS.patch. `mass-rs/scripts/check_official_client.py` reverses
the patch and verifies the pristine upstream files. All other Codex crates retain
the same fixed revision. The [bounded event/RPC/frame policies](../design/native-runtime-connectors.md#native-ticket-06-bounded-core-delivery-and-shared-transport-pressure) supersede the
original upstream unbounded consumer behavior. Unknown valid notification variants
are explicitly ignored and recorded by the optional acceptance observer; damaged known payloads fail the attributed current attachment or,
when unattributable, the host. The acceptance recorder captures the real official
SDK API boundary; typed serialization can normalize fields, so wire.jsonl is not
a byte-level socket or internal handshake capture. Stop uses owned TERM/KILL
cleanup when needed, independently of protocol interrupt. See
[official source provenance](../../mass-rs/vendor/codex-app-server-client/MASS-README.md).
