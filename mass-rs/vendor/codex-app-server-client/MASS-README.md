# Fixed official client, bounded remote consumer patch

Origin: OpenAI Codex `d27764b82f7118f674371e6d6e76271d9d606edb`,
`codex-rs/app-server-client`, version 0.160.1. Apache-2.0 LICENSE and
upstream NOTICE are retained. UPSTREAM.json records pristine file hashes.
Cargo.toml.upstream preserves its inherited manifest. The build manifest resolves
that inheritance explicitly; every Codex dependency remains on the same git rev.
All in-process implementation, handshake, typed requests, RPC routing and server
request handling remain official code. No remote-only rewrite or alternate SDK
exists. Cargo patches only this crate and treats it as an explicitly identified
vendor workspace member so its original and added tests are checked.

MASS.patch is the complete diff from the fixed source, including manifest
resolution and tests. `scripts/check_official_client.py` reverses it in a temporary
copy and verifies every pristine hash without the original checkout or network.
The generated notification method list is copied from the fixed protocol macro;
its source hash is embedded. It distinguishes unknown valid methods from damaged
known typed payloads without parsing error-message text.

The remote event queue has finite count and byte permits, including initialization
pending events and the consumer's in-flight event. Initialization overflow fails
because there is not yet a consumer. Steady-state pressure waits and reaches the
shared WebSocket; it can delay other threads, RPC replies and approvals. Complete
notifications are not silently dropped. An individually oversized event or RPC
result/error fails explicitly. WebSocket frame/message protection remains 128 MiB;
this is separate from consumer byte permits and temporary decoding memory.

Remote shutdown has one total 5 second deadline including command enqueue, then
aborts and awaits its worker. A worker ownership guard aborts on cancellation or
client Drop. It does not own the native OS child: MASS still owns wait/reap and
TERM/KILL fallback. No finite file-I/O guarantee follows from this SDK deadline.

Tests added to the official real-WebSocket helpers cover initial/count/byte
limits, current notification retention, shared RPC delay, shutdown, cancellation,
unknown versus damaged-known notifications, bad JSON and an oversized frame.
They are SDK transport tests, not substitute real-Agent product acceptance.
