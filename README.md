# MASS

[harness](harness/index.md) 是指导当前项目演进和发展的唯一真相源，包括 [agent 工作原则](harness/principles/index.md)、架构、深层模块设计与 API 合同。agent 写入 harness 前须有人类明确要求或事先同意。
以下运行与构建说明描述现有实现；不代表实现已符合新版设计。

The Rust implementation serves native Codex text and commandExecution sessions over HTTP/SSE.
It supports isolated concurrent threads on one host per runtimeName, once-only
command approvals, cancellation, per-thread archive, explicit strict recovery,
bounded fact delivery and owned runtime Stop. See the [HTTP binding draft](harness/bindings/http.md)
and [testing principles](harness/principles/testing.md) for the development contract. Codex is the only
implemented provider; configuration mutation, questions, background lifecycle and
database projections remain unavailable. Rust contains no ACP transport/SDK/parser,
configuration, fallback or executable ACP acceptance entry point. Retired v1 code, historical research and raw private evidence are absent from this source snapshot.

The [Go implementation](mass-go/README.md) currently provides the Ticket02
Codex SDK-mock vertical path: no-input native identity, separate text input,
file-persistent history and live Service observation. Production Go packages live
under `mass-go/pkg/`; `cmd/` contains the entrypoints. `make build-go` builds
`bin/massd-go` (the synthetic workflow) and `bin/massctl` (local read-only queries).
The Go CLI no longer calls the Rust server; Go HTTP/Claude/recovery support belongs
to later tickets. `make test-go` runs the offline SDK/Store/application suite;
root Make checks include the Go slice and require Go 1.26.9. The independent
[SDK protocol probes](mass-go/examples/sdkprobe/README.md) are retained.

The Rust workspace lives entirely in [mass-rs](mass-rs/README.md), leaving the
repository root available for other languages. Run `make fmt`, `make lint`,
`make build`, `make check` from this directory. Rust 1.95+ is required.
Run `cargo run --manifest-path mass-rs/Cargo.toml --locked -- --listen 127.0.0.1:24368 --config /absolute/config.json`.
The actual bound address is printed; logs go to stderr. Ctrl-C/SIGTERM stops
activated runtimes. Health: `/livez`, `/readyz`.

Configuration registers named runtime instances, with an explicitly reviewed binary hash:

```json
{
  "dataDir": "data",
  "runtimes": [{
    "name": "codex",
    "type": "codex",
    "config": {
      "command": "/absolute/path/to/codex",
      "sha256": "09fa44fdc37a5fc70dc1ace31235f90468a2e193d0e85f7552eab068ea2582be",
      "env": {}
    }
  }]
}
```

The path points to the fixed native binary, not the ACP adapter. Only codex-cli 0.160.1 is supported. Actual executable bytes must match sha256. Environment/auth/provider are inherited with optional process env overrides; no global config/auth writes occur. Environment values are not persisted in metadata. dataDir resolves relative to the configuration directory, or the daemon cwd without a config.

Registration uses only `name`, `type`, `config` and optional `enabled` (default true).
`codex` and `codex-remote` are implemented. Unknown fields, duplicate names and retired `agents`/`provider`
configuration fail before registration. `command` is an absolute path and `sha256` is required;
binary bytes and fixed version are checked only on activation. `args` defaults to an empty
array; only `--strict-config` and `--analytics-default-enabled` are accepted. Other or
duplicate arguments fail explicitly, including overrides of protocol options.
`env` defaults to an empty map of nonsecret values. `secretRefs` maps child environment keys
to `env:NAME` references resolved at activation; overlap with `env` fails. Host environment
inheritance remains in effect and does not isolate credentials.

`codex-remote` connects to an existing Codex 0.160.1 app-server with
`config: {"url":"ws://127.0.0.1:PORT","authRef":"env:REMOTE_AUTH"}`.
`authRef` is optional and is resolved only at activation. Tokens require `wss` or
loopback `ws`; URL userinfo, query strings and fragments are rejected. Remote accepts
only `url`, `authRef`, `closeTimeoutMs`, `factLaneCapacity` and `sdkEventBytes` (the
same capacity bounds/defaults as local). It rejects local launch/environment fields.
Session cwd is an absolute path in the remote filesystem; MASS does not check its
local existence. Configure only an authorized, trusted service.

Remote shares the local adapter's commands, approvals, cancellations, normalized
facts and private evidence validation. It owns its connection and local worker,
never the server process. Stop closes only that connection. A stopped instance
proves local cleanup; it does not prove remote work ended. Without native thread
release evidence archive remains `close_unknown`, including after transport EOF.
Other clients remain independent. Remote `capabilities.resume` is always false;
explicit resume returns `unsupported_capability` before connection or tail repair.
No cross-connection/cold recovery is supported because the handshake cannot prove
service/account/data ownership. URL, returned ID and version claims are insufficient.
Existing offline history remains readable. Loopback real protocol acceptance does
not certify authenticated remote deployment or WSS certificate/authentication setup.

The JSON-RPC methods `agentd/runtime/types`, `agentd/runtime/list` and
`agentd/runtime/status` (params `{"name":"codex"}`) are read-only and never activate an Agent.
Discovery returns names, types, enabled flags and `status.phase`/`unavailableReason`, without
execution configuration or secrets. New registrations start `pending`, including disabled
instances. Disabled instances reject new and resumed sessions. `/livez` observes daemon
liveness; `/readyz` returns 200 for zero instances or at least one enabled pending/ready
instance, otherwise 503. `agentd/status` uses the same readiness decision.

`runtime-registry.json` stores stable internal identities, configuration digests and versions;
it contains no raw configuration or expanded secrets. Missing entries in the configuration
remain in this ledger. Identical re-registration restores the original identity; execution
configuration changes fail while persisted sessions reference it. Missing or corrupt identities
fail rather than claiming old sessions. A process-held OS lock prevents concurrent daemon
ownership of this directory. Keep this ledger with the session data. Old session bindings
cannot be adopted or migrated by this configuration change. There is no online CRUD or reload.

Activation is lazy on new or explicit resume. Optional per-runtime closeTimeoutMs sets the total thread-close deadline (1..300000 ms, default 30000). Local Runtime owns the app-server process at cwd `/`; thread/start owns each session cwd. It uses persistent paginated threads, experimental API opt-in, workspace-write sandbox and untrusted approvals at thread scope. commandExecution produces complete call/result facts; supported native command approvals receive once-only accept. Other control requests are explicitly rejected and unsupported native item types fail the attachment. Creation sends no model input. Read queries never start runtimes.

Concurrent first creations on the same runtimeName share one activation/handshake. Distinct local runtime names keep separate hosts even with identical executable/config values. Remote names own independent connections and can point to the same shared service. Each attachment has its own admission, state, thread and Journal; `agentd/status.connected` counts online attachments. Creation reserves a name briefly without holding the session map across native open. Failed native opens or metadata commits release only their provisional resources. A timed-out open keeps its name reserved until its native outcome and any required thread release settle; other names and healthy attachments can continue.

Create with JSON-RPC method `agentd/session/new`, params `{ "sessionName":"demo", "spec":{"runtimeName":"codex","cwd":"/absolute/existing/directory"}}`, then prompt with `{ "sessionName":"demo", "prompt":[{"type":"text","text":"hello"}]}`. See the API for native_recorded receipts and live-only SSE.

Only original native complete items enter the append-and-flush Journal. Preview deltas are never accumulated, replayed or promoted on idle/EOF. Metadata uses formatVersion=2 with the real Agent-returned sessionId and private stable-instance/recovery bindings. The internal storage locator safely scopes that opaque ID to its instance. Each JSONL record saves a schemaVersion=2 public event together with versioned private adapter evidence. History, SSE and Handlers receive only the public projection after append and flush. Old metadata/event formats explicitly fail as unsupported and remain preserved.

Rust native acceptance: set `MASS_TEST_CODEX` to the reviewed native binary and
`MASS_TEST_CODEX_SHA256` to its reviewed digest, then run `make test-native`.
It explicitly uses existing auth and real models. `make test` runs the offline
module integration and official SDK transport group. See [testing and acceptance principles](harness/principles/testing.md).
The Rust aggregate records the selected test names, exit results, source hashes,
SDK observations, and a second owned-process identity readback. Production massd
has no acceptance recorder or fault configuration.

Select groups with `NATIVE_TEST=migrated_create make test-native`,
`NATIVE_TEST=migrated_tools make test-native`, or another test-name substring.
The full group also requires `MASS_TEST_MASSD` to name a native release `massd`
with its adjacent manifest. `MASS_TEST_CODEX_HOME` optionally selects an existing
absolute Codex auth/provider configuration directory for release acceptance.

See the [Runtime design](harness/modules/runtime/design.md) and [glossary](harness/glossary.md). Database/query projections remain unavailable.



Official Rust client/protocol are fixed to Codex revision `d27764b82f7118f674371e6d6e76271d9d606edb`; runtime launches the fixed binary on a private Unix socket WebSocket endpoint and the official client owns handshake/routing. Rust 1.95+ is the supported build baseline. No /tmp path dependency, copied SDK, or hand-written framing is used. The upstream client transitively includes embedded-server crates even though MASS uses only its remote client. The retained official client has a reversible repository-local bounds/shutdown patch; its pristine source and patch are verified by make check. `CARGO_FLAGS` defaults empty; any local `--ignore-rust-version` experiment must be explicit and is not a supported-toolchain pass.

The product is a Cargo workspace with seven responsibility crates and the daemon
composition package. See the [module architecture](harness/architecture.md)
and [testing principles](harness/principles/testing.md). Direct Cargo
build/test commands from this directory require `--manifest-path mass-rs/Cargo.toml
--workspace --all-targets`; the Make entry points include these flags. The
acceptance source hash inventory includes every crate source and manifest.


`make test` and `make check` supply a 16 MiB test-thread stack and two test threads
by default (`RUST_MIN_STACK=16777216`, `RUST_TEST_THREADS=2`, both overridable).
The fixed official embedded-client tests overflow Rust's default 2 MiB test
stack. These values apply only to the cargo test process; production massd needs
neither variable. All 38 retained official tests run.

Run the full current-product matrix with `make test-native`, using the reviewed
native Codex identity and release executable described above. All scenario
assertions and orchestration execute in Rust. Tests use isolated workspaces and
retain private evidence under `MASS_TEST_EVIDENCE` (or the system temporary
folder). Keep failed attempts. Native children and explicit acceptance teardown
are accounted separately by PID/start identity.
The current macOS native release gate uses `make release` and `make verify-release`. Stage acceptance alone
does not establish release linking or execution. Linux and cross-compilation
were deferred by the user.

## Existing installations and source relocation

The root Makefile delegates to `mass-rs/Cargo.toml`; there is no root Cargo
workspace. The daemon package/import names are `mass-daemon` / `mass_daemon`;
the binary is `massd`. See the [development documentation](harness/index.md).

Existing data stays in its original directory, including local `data/` and
`rustv2/data/`. Without a config, the daemon selects `<launch-cwd>/data`.
Using `--manifest-path` from the repository root keeps that launch cwd; running
from `mass-rs/` selects `mass-rs/data`. For an existing installation, pass
`--config /absolute/config.json` with `dataDir` set to the original absolute
directory. Relative `dataDir` remains relative to the config file's parent.
One daemon owns each directory. Source relocation never moves or converts data.

Development requires Rust 1.95+; reproducible native macOS release builds require Rust 1.96.0. Linux delivery remains deferred and unverified.
