# MASS SDK simulation evidence route

SDK-FU-01 compatibility investigation, 2026-11-02. This is an implementation
runbook, not a harness contract or a replacement for [acceptance.md](acceptance.md).
SDK-FU-02 owns driver/business assertions; SDK-FU-03 owns opening cleanup;
SDK-FU-05 owns the eventual integrated simulation run.

## Decision And Evidence Classes

**Neither provider has a compatible admitted recording in the bounded selection.**
Both selected continuation recordings reproduce an ordinal-1 strict mismatch.
Official-recording MASS acceptance is BLOCKED, not passed. Later incompatibilities
below are source/encoding findings, not executed later-ordinal failures. Do not
infer that the entire corpus is incompatible from this eight-capture inspection.

| Evidence | Permitted conclusion | Not established |
| --- | --- | --- |
| Source inspection and public-type encoding | Request shapes and matcher compatibility conditions | Execution or business PASS |
| SDK-only Go simulation or official-SDK replay | The named SDK/driver consumed the selected material | MASS Service/adapters passed |
| Strict authored static fixture | MASS works against explicitly synthetic, exact protocol frames | Official recording, real native behavior |
| Authored stateful protocol peer | MASS handles the peer's declared dynamic correlation rules | Unmodified SDK strict-matcher consumption or native UUID echo guarantees |
| Admitted recording through MASS | Business assertions plus full consumption of unchanged admitted bytes | New live execution, unselected cases or whole-corpus coverage |
| Live Agent/model | Only the explicitly executed native scenario | Authorized here: none |

`simulate` is a runner mode, not proof of provenance. Its verifier also accepts
synthetic materials without recording manifests. A synthetic scenario MUST say
`"provenance":"synthetic"`; never fabricate capture/adoption/official-replay
records or copy edited capture frames into a new directory and call them official.

## Immutable Inputs And Retained Evidence

Investigation starts at MASS `8a48a628c71259aa5a2ac688334a8d5d8cf6a261`, exactly
the initial `codex-sdk-followup-integration` tip, on `codex-sdk-fu-01`.
The fixed module is `github.com/zoumo/agent-sdk-go`
`v0.0.0-20261101172223-619cf8ac7e14`, source
`619cf8ac7e1484f82c40d6fd96155fab9404e634`; no `replace` or mutable SDK checkout.
Observed tools: Go 1.26.9 darwin/arm64, RTK 0.42.4, built testify
`0.1.0-dev` (bind the binary hash, not that non-unique version string).

Capture directories are relative to the module's `tests/simulation/recordings/`:

| Provider | SDK-US-001/SDK-CASE-001 capture | `raw/raw.jsonl` SHA-256 |
| --- | --- | --- |
| codex | `20261101T065559Z-6a7c186793d249cb8595d5a057c65941` | `4cef5e38ca34351a9334c4642d446611478418155bb61cbdc7a0a27da5a8eca7` |
| claude | `20261101T065847Z-7f3b2b919eef44a091d5ce4e539406d1` | `f82aab3884357d79311055f71b084153f2e82b6643f63387af3d436bcee4d344` |

Local shared evidence root (outside Git):
`/Users/jim/code/zoumo/mass-sdk-worktrees/followup-evidence/01/`.

- `selected-source-hashes.json`: prior provenance, all listed selected source
  files rehashed successfully before reproduction, not just the raw stream.
- `prior-ordinal-1/`: byte-preserved diagnostic files from trim's
  `.scratch/sdk-trim/simulation/latest-attempt-004`; stores/projects and executable
  launchers are omitted. Original paths inside JSON remain historical evidence.
- `bounded-scan.json`: exact eight capture IDs, profiles, prompts, every source
  file hash, and all client requests. `source-binding.json` binds the index,
  relevant SDK sources and MASS source files. Index admission references checked;
  this is not a new qualification/admission or an official replay.
- `audit.py`: read-only bounded audit, repeatable with repository and output
  paths as arguments. `wire-shapes.go` / `wire-shapes.json`: public Codex types
  encoding only; no SDK session or native executable is opened.
- `reproduce-001/`, `reproduce-001.log`, `reproduce-001.exit`: new execution at
  this baseline, 0/2, exit 1, both `peer-stderr.log` files report
  `ordinal 1: client mismatch`. No business completion or consumption receipt.
- `report.md`, `validation.json`, `evidence-sha256.json`: handoff, validation and
  evidence integrity inventory. Hashes bind artifacts, not successful behavior.

## Compatibility Findings

### Codex

1. Recorded ordinal 1 is `initialize` with ClientInfo
   `official-python-story-recorder`, title `Protocol recording`, version
   `0.161.0`, and `capabilities.experimentalApi=true`. MASS sends
   `{"clientInfo":{"name":"mass","version":"2"}}` as params, without
   capabilities. Source: `pkg/runtime/codex/driver.go`, `Driver.Start`;
   pinned SDK `codex/client.go` does not insert experimental capabilities.
   The matcher remaps request IDs, NOT ClientInfo/options. Do not impersonate
   the Python recorder or enable capabilities solely to match a capture.
2. Recorded ordinal 4 `thread/start` requests `ephemeral=true`, sandbox,
   approvalPolicy, model, baseInstructions and developerInstructions. MASS's
   fresh path sends only cwd and `ephemeral=false` for this Session spec.
   Persistence/recovery identity checks reject ephemeral threads. Merely fixing
   initialization would not make this capture usable; keep product persistence.
3. Recorded ordinals 9/42 `turn/start` text inputs omit `text_elements`.
   MASS explicitly sends `text_elements:[]`; public pinned-type encoding confirms
   that presence. The strict JSON comparison preserves absent versus empty.
   Prompt text and recorded threadId can agree without requests being equal.
4. Native response identity must still supply a nonempty thread ID, session ID,
   matching cwd and non-ephemeral context. Native turn ownership/completion,
   `thread/unsubscribe` and `thread/closed` cleanup must be checked independently
   of string replies. No later step was executed on the admitted capture.

### Claude

1. Recorded ordinal 1 initializes with `{"subtype":"initialize","hooks":null}`.
   MASS uses nil Initialize options, so pinned `claude/client.go` adds
   `forwardSubagentText:true`. Omission differs from true under the matcher.
   Keep this observation policy; do not pass empty options just to match.
2. Recorded ordinal 5 first text input uses `session_id:"default"`, null parent,
   and no UUID. That basic shape agrees with MASS's fresh single-text input.
   The driver passes the original recorded project path as cwd; the runner
   separately materializes writable files under its new workspace. Do not
   rewrite recorded cwd/IDs or use materialized cwd as a protocol substitute.
3. Recorded ordinal 18 second text input has no UUID. MASS generates a new
   work UUID and includes it as `uuid` on subsequent inputs. The pinned Claude
   matcher maps control request IDs only, not user UUIDs. An independently
   authored *static* transcript with a fixed UUID would also fail nondeterministically;
   neither omission nor a wildcard is an available strict-matcher feature.
4. Recorded results at ordinals 16/29 have distinct native UUIDs and no
   `user_message_uuid`. First fresh submission can use single-submission
   attribution; the second cannot. MASS must retain unknown work rather than
   infer idle from an unlinked result. Need recognized linkage for the second
   submission, not merely a second nonempty assistant message or a result frame.
5. `system/init` supplies the native session ID and cwd. Saved identity, a user
   input write, hook/status events or stream EOF cannot replace that check.
   Reproduction retains `cleanup.json` with `released:false`; an empty OS process
   readback is not logical cleanup. SDK-FU-03 must resolve that responsibility.

### Bounded Alternatives Checked

For **each provider**, inspected indexed/admitted cases 001, 002, 004 and 005
(eight captures total). Case 001 is continuation; 002/004 are structured-input
with image content; 005 is input while active. All four Codex captures have the
same recorder initialization and ephemeral start policy; all four Claude captures
omit forwarding in initialize. Case 005 also lacks user UUIDs. Cases 002/004
require unsupported image input; none is a replacement for two sequential text
turns. The current MASS simulation driver explicitly accepts only case 001,
continuation, exactly two prompts. Do not relabel another case to bypass this.
Exact alternative capture IDs and hashes are in `bounded-scan.json`.

## Reproduce Without Native Calls

Run from the MASS repository root. Use a fresh absolute evidence/output path,
Go dependencies already cached, and no personal data directory. Only build the
pinned module; do not run its record/gate script or discover host Agent binaries.
The source plan's `cli`/`model` fields are historical, NOT execution targets in
`simulate`; the MASS driver uses only the absolute runner-supplied peer.

```sh
rtk proxy sh -eu -c '
E=$(mktemp -d /tmp/mass-sdk-route.XXXXXX)
export GOWORK=off GOPROXY=off GOSUMDB=off
SDK=$(go -C mass-go list -m -f "{{.Dir}}" github.com/zoumo/agent-sdk-go)
go -C mass-go list -m -json github.com/zoumo/agent-sdk-go > "$E/module.json"
go -C mass-go build -o "$E/mass-sdk-driver" ./tests/sdksimulation
go -C mass-go build -o "$E/agent-sdk-testify" github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify
"$E/agent-sdk-testify" --version > "$E/tool-version.txt"
python3 - "$SDK" "$E" <<\PY
import hashlib, json, pathlib, sys
sdk, out = map(pathlib.Path, sys.argv[1:])
selected = [
 ("codex", "20261101T065559Z-6a7c186793d249cb8595d5a057c65941",
  "4cef5e38ca34351a9334c4642d446611478418155bb61cbdc7a0a27da5a8eca7"),
 ("claude", "20261101T065847Z-7f3b2b919eef44a091d5ce4e539406d1",
  "f82aab3884357d79311055f71b084153f2e82b6643f63387af3d436bcee4d344")]
cases, hashes = [], {}
for provider, capture, expected in selected:
    root = sdk / "tests/simulation/recordings" / provider / "SDK-US-001/SDK-CASE-001" / capture
    assert hashlib.sha256((root / "raw/raw.jsonl").read_bytes()).hexdigest() == expected
    cases.append({"case": str(root)})
    hashes[str(root)] = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
                        for p in sorted(root.rglob("*")) if p.is_file()}
(out / "cases.json").write_text(json.dumps(cases, indent=2) + "\n")
(out / "source-hashes.json").write_text(json.dumps(hashes, indent=2) + "\n")
(out / "binary-hashes.json").write_text(json.dumps({n: hashlib.sha256((out/n).read_bytes()).hexdigest()
    for n in ("mass-sdk-driver", "agent-sdk-testify")}, indent=2) + "\n")
PY
git rev-parse HEAD > "$E/mass-head.txt"
git status --short > "$E/mass-status.txt"
set +e
"$E/agent-sdk-testify" simulate --cases-file "$E/cases.json" \
  --driver "$E/mass-sdk-driver" --output "$E/attempt" --jobs 2 --timeout 60s \
  > "$E/attempt.log" 2>&1
code=$?
set -e
printf "%s\n" "$code" > "$E/exit-code.txt"
printf "Evidence: %s\n" "$E"
cat "$E/attempt.log"
exit "$code"
'
```

Expected baseline outcome: exit 1, selected=2, passed=0. Inspect per-provider
`peer-stderr.log`, `result.json`, `summary.json`, `cleanup.json` and the batch
summary; missing `replay-peer.json` is expected on matcher failure, not success.
Afterward rehash every source against `source-hashes.json`, compare source/binary
bindings, and inspect `rtk proxy ps -axo pid=,ppid=,command=` for owned commands
containing that unique output path. Preserve failures; never overwrite an attempt.
CLI exit 0 from the MASS driver only means it wrote result.json; check its status.

## Authored Route For SDK-FU-02 (Design, Not Executed PASS)

Use the same two literal prompts as case 001: `Reply with the word Ready.` then
`Reply with the word Done.` Expected synthetic replies are exactly `Ready` and
`Done` (authored expectations, not the official capture's punctuation).

- **Codex static strict route:** author new `plan.json` with provider codex,
  caseId SDK-CASE-001, driverProfile continuation, an isolated absolute project,
  the two prompts, and empty scenario files. Author `raw/raw.jsonl` as a v1 JSON
  object with `version:1`, `provenance:"synthetic"`, and `frames`. Each frame has
  increasing ordinal, direction c2s/s2c, channel protocol, epoch 1, and object wire.
  Match MASS's exact initialize, initialized, non-ephemeral thread/start, two
  turn/start inputs including `text_elements:[]`, and thread/unsubscribe. Supply
  independently authored identity/cwd, two distinct turn IDs, full assistant items,
  turn completions, unsubscribe response and thread/closed. Only request IDs use
  the SDK's existing remapping. Run the built testify `simulate --materials
  /absolute/authored-codex --driver /absolute/mass-sdk-driver --output
  /absolute/new-attempt --timeout 60s`. This format is supported by the pinned
  parser; the specific success fixture still belongs to 02 and is NOT supplied
  or validated by 01. Keep it outside admitted recording directories.
- **Claude stateful authored route:** a fixed static transcript cannot consume
  fresh UUIDs with this unchanged matcher. For 02's product-only success test,
  use a separately labelled executable synthetic peer, not `protocol mock` or a
  forged testify receipt. Validate exact message keys, types, initialize forwarding,
  both literal prompts, default session target, null parent, no first UUID, and a
  valid fresh second UUID. Bind that UUID once and echo it as
  `user_message_uuid` in second-turn assistant/result observations on the same
  synthetic native identity. Reject unknown keys, extra input, missing/out-of-order
  messages and truncated EOF. An arbitrary ignored UUID field is not strict
  validation. This is a stateful test protocol, NOT a change to the SDK matcher.
  Its assumed echo must be stated as authored behavior, not native evidence.
- To exercise that peer through the existing driver, create `materials/case.json`
  with provider, caseId, driverProfile, project and prompts, then a request JSON
  with `case:"SDK-CASE-001"`, absolute materials/workspace/output, and
  `peer:{"command":"/absolute/authored-peer","args":[...]}`. Invoke
  `rtk proxy /absolute/mass-sdk-driver --request /absolute/request.json`.
  Read result and a separately named synthetic-peer receipt. Do not call that
  receipt `replay-peer.json` or run corpus adopt/qualify on synthetic bytes.
  Peer implementation and executed success assertions remain work for 02.

For both authored routes, 02 must assert two *separate*, correctly ordered inputs
and completions, exact reply content, no second completion before the second
input, original native identity/connection, committed public history with correct
namespace/session routing, two settled public idle boundaries, no SessionError,
and proper cleanup. Counting two arbitrary nonempty messages is insufficient.
Verify final history after the second boundary and stop; retain result, receipts,
history and lifecycle evidence. Independently corrupt expected prompt/order/reply
in test-owned materials to prove assertions reject wrong behavior; never mutate
an admitted capture. This ticket writes no product tests; code TDD is not
applicable to its documentation/source investigation.

## Full Consumption, Cleanup And Unblocking

For a native strict-mock run require **all**: successful batch and case summary
without error, business `result.status=passed`, every selected connection receipt
with `fullConsumption=true`, `clientEOF=true`, `nativeLaunched=false`, matching
provider and raw SHA-256, and no extra/unanswered client messages. A fixture or
SDK-only PASS cannot substitute for those receipts. For a stateful authored peer,
report its own EOF/state-machine checks separately; it cannot attest consumption
of an admitted static recording.

Require MASS `cleanup.released=true` as well as owned-process exit/reaping and
released store handles/listeners. A killed peer does not settle Service opening
responsibility. Retain `released:false` from both prior and reproduced Claude
failures for 03; do not silently reclassify it after an OS-only cleanup check.

The minimum external decisions/materials to unblock **official** acceptance are:

1. Codex: an independently sourced/admitted capture made with genuine MASS
   ClientInfo, actual capabilities, persistent thread semantics and exact text
   input shapes, followed by official replay/provenance review. Capture origin
   must not claim the Python recorder sent bytes it did not send. Requires a
   separately authorized recording workflow; merely updating a Go SDK pin or
   replacing ClientInfo does not solve the remaining differences.
2. Claude: native evidence with forwarding enabled and demonstrated subsequent
   work linkage, plus an explicitly approved SDK/tool contract for binding fresh
   user UUIDs to recorded UUIDs and linked outputs. Existing matcher has no such
   facility; even a new fixed-UUID capture alone cannot guarantee reproducibility.
   Alternatively authorize a different evidence goal (stateful synthetic only),
   explicitly leaving recording-driven MASS acceptance blocked. Do not invent
   native UUID echo guarantees to declare it solved.

No such SDK/matcher work, recording, real Agent/model call, or evidence-goal
change is authorized here. SDK-FU-05 can reproduce and archive the known failures
now; it cannot honestly report dual-provider admitted-recording success from
these materials. Parent owns tracker updates and any request for external scope.
