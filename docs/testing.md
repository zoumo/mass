# Testing

Offline Rust tests verify deterministic contracts and never substitute for explicit real Codex/model acceptance.

The priority is module integration and end-to-end behavior, not unit-test counts.
Each responsibility crate owns public-interface tests in its `tests/` directory.
Only the private Codex delivery lane has focused unit tests for permit ownership,
ordered pressure, sealing and drain; public tests cannot directly saturate that
internal boundary without a real native producer.

## Execution groups

- `make test`: offline module integration, isolated daemon TCP health, and the
  retained official SDK transport tests. Loopback TCP/Unix sockets must be allowed.
  No real models or personal runtime data are used.
- `make test-native`: explicit real native group. Set `MASS_TEST_CODEX` to the
  reviewed absolute Codex 0.160.1 executable and `MASS_TEST_CODEX_SHA256` to its
  reviewed digest. The full group also requires `MASS_TEST_MASSD` with an adjacent
  native release manifest. Existing native auth/provider access is required for
  model cases. Missing prerequisites fail; they are never silently skipped.
- `NATIVE_TEST=<substring> make test-native` selects a case. A filtered run only
  certifies the selected cases, not the full group.
- `MASS_TEST_MASSD=/absolute/release/massd NATIVE_TEST=migrated_release make
  test-native` runs the copied production release with system-only PATH, private
  HOME, online inputs/SSE, unavailable-Codex offline reads and explicit cold resume.
  `MASS_TEST_CODEX_HOME` optionally selects an existing absolute native auth/config
  home; otherwise `CODEX_HOME` or the user's `.codex` is used. No credentials are
  copied into evidence. Offline health always uses Cargo's freshly built daemon.
- `make check`: structure/provenance/docs, fmt, all-feature lint, offline tests
  and all-target build. It does not certify the real native group.

Native module lifecycle acceptance lives in `mass-rs/codex/tests/native.rs`.
Cross-crate TCP JSON-RPC/SSE, Service, SDK, Journal and Handler cases live in
`mass-rs/tests/native_codex.rs` and `tests/scenarios/`. Production daemon copied-data
and release cases live in `tests/http_health.rs`. Real tests are `#[ignore]` with
explicit reasons in the offline group. `make test-native` compiles and inventories
the test executables before `tests/native_stage.rs` runs the selected ignored tests
directly, single-threaded. It rejects an empty selection, compares the discovered
case count with successful libtest results, records source hashes and verifies
that the source did not change during the run. It performs a second PID/start
readback across recorded cleanup reports. Construction observation gates delay
real operations and never invent native facts.

Every test owns temporary workspaces/data and uses ephemeral ports/private Unix
sockets. Native evidence directories are retained and printed (optionally under
`MASS_TEST_EVIDENCE`). The common native runner catches assertion panics, shuts
down, records SDK/HTTP/Handler observations and checks owned PID/start identities.
Shutdown tests drain accepted facts and require SSE EOF. Offline daemon health
uses a private cwd/data directory and reaps its child on Drop.

| Crate | Boundary and required behavior |
|---|---|
| mass-domain | Persisted camelCase metadata, identity/configuration round trip, required fields |
| mass-runtime | Text validation before admission, exact attachment ownership, cancellation, structured errors, lost observer, public identity |
| mass-journal | Atomic metadata, binding validation, ordered pagination, corruption rejection, uncommitted tail |
| mass-events | Commit before publication, preview isolation, sealed failed writer, strict recovery, bounded subscribers, Handler order/isolation/shutdown |
| mass-codex | Reviewed configuration/binary admission, native payload validation, bounded fact delivery, order, drain, resource permits; real SDK lifecycle separately |
| mass-sessions | Offline queries, capabilities, pagination, lifecycle validation, rollback/name reservation, shutdown |
| mass-http | JSON-RPC validation/IDs/errors/notifications, health and readiness, live-only watch |
| mass-daemon | Configuration composition and daemon startup; cross-crate real native acceptance |
| official SDK vendor | Existing upstream and MASS transport tests; retain audited source patch unchanged |


The separate Go mock CLI suites are described in [mass-go](../mass-go/README.md). Native macOS arm64 is the release target; Linux delivery remains unverified.
