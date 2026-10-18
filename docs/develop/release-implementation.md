Deployment contract: [single executable](../design/single-file-delivery.md).

# Single-file deployment

The production composition is the `mass-daemon` crate in `mass-rs/src`, built as
`massd`, plus the seven responsibility crates under `mass-rs`. Delivery does not introduce an installer,
self-extracting wrapper, alternate runtime path, ACP client or provider. The
fixed official Codex client/protocol revision and existing runtime patch behavior
remain unchanged. The retained client includes a test-only recursion-limit adjustment for optimized upstream tests. Embedded upstream client code stays present in the source;
the release linker can discard unreachable code normally.

The build entry point supports native macOS arm64 only:
`aarch64-apple-darwin`. Linux and cross-compilation remain unverified.
macOS links third-party code statically and uses system libraries
from `/usr/lib` or `/System/Library/Frameworks`. macOS is not fully static.

`make release` pins Rust 1.96.0, uses `mass-rs/Cargo.lock` with `--locked`, builds only the
production `massd`, preserves `panic=unwind`, and copies a verified executable to
`dist/<target>/massd`. It emits a manifest with source fingerprints, SDK revision,
build tools/environment, SHA256, size and final link inspection. Ambient Rust
flags and profile overrides are rejected. The existing static lzma feature
remains enabled. The actual
dependency graph and final linker output determine which upstream native
libraries are linked; Rust's target name alone is not sufficient proof.

The link gate rejects an unexpected architecture, any non-system Mach-O install
name, or LC_RPATH. It also checks the explicit macOS 14.0 deployment target and
executes `--help`. The precise tested macOS version belongs in the validation
report, independently of the declared baseline. Rust dependencies are fixed by
lock/revision; the native compiler/SDK are recorded in each manifest.
Reproducible inputs/commands do not
claim byte-identical output across different Apple SDKs or source paths.

The deployed MASS program requires no Rust, Python, Node, package manager,
source checkout, build cache or extra shared library. Writable configuration,
credentials, data directories and explicitly configured external Codex 0.160.1
are deployment inputs. Codex's own package/resources and OS/network requirements
remain separate. Offline read queries do not require a working Codex executable.

The Rust `release::migrated_release` test (`NATIVE_TEST=migrated_release make
test-native`) copies only production massd into a new directory,
launches with a system-only PATH and no acceptance constructors, and tests real
native text, correlated native_recorded receipts, complete replies, exact
Journal/history/durable-SSE equality, Stop, offline restart, explicit cold resume
with stable native identity and a genuine append preserving the old byte prefix,
then observed owned PID/start cleanup. The persisted nativeItem is the production observation; this
runner does not claim raw socket capture or construction-only Handler coverage.
Earlier stage acceptance retains Handler/panic/shutdown coverage. A receipt is
not proof of a Journal commit or model consumption; the later history/Journal
comparison is separate evidence.

See [build/run instructions](../single-file-delivery.md) for the release recipe
and platform acceptance record.

`make verify-release` also checks: in addition to binary/hash
and Mach-O inspection it compares the full recorded source file set and hashes
with the current checkout and rechecks the reversible official-client patch.
Use the Ticket 07 completion record for current acceptance; prior artifacts do
not certify changed source. `RELEASE_DIST` can select an isolated output directory.
