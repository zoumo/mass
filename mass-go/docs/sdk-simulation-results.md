# Historical SDK-FU-05 Independent Simulation Results

> **Historical binding.** This result binds MASS `bb3f09cea046` and SDK
> `619cf8ac7e14`, including the former SDK-owned testify tool. It is not a result
> for the current `a3a99ad44b90` pin or the external prepared-mock adaptation.
> Preserve these failures as history; do not cite them as current adaptation PASS.

Official recording-driven acceptance: **BLOCKED**. SDK-FU-05 is not closed.
Executed independently on 2026-11-02 (Asia/Shanghai); no SDK-FU-02 success log
is counted as an execution here. Procedure and compatibility analysis:
[sdk-simulation-route.md](sdk-simulation-route.md).

## Binding And Scope

- Worktree: `/Users/jim/code/zoumo/mass-sdk-worktrees/sdk-fu-05`,
  branch `codex-sdk-fu-05`, initially clean.
- Executed MASS source: `bb3f09cea0465631093543024c505eb3f7199edb`.
  Requested integration base `bb3f09cea` is an ancestor (and was HEAD).
- SDK: `github.com/zoumo/agent-sdk-go`
  `v0.0.0-20261101172223-619cf8ac7e14`, source
  `619cf8ac7e1484f82c40d6fd96155fab9404e634`; no replacement.
- Built current `./tests/sdksimulation` and fixed SDK
  `github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify` from `mass-go/`,
  with `GOWORK=off GOPROXY=off GOSUMDB=off`. Go 1.26.9 darwin/arm64;
  testify reports `0.1.0-dev` (binary hashes below disambiguate it).
- Execution/documentation only: TDD process read; no invented code RED/GREEN.
  No code, matcher, SDK, capture, native Agent/model, or harness changes.
  Full 15-negative rerun and final offline gate belong to the parent, not this run.

Evidence root (all commands, binaries, logs, inputs and bindings outside Git):
`/Users/jim/code/zoumo/mass-sdk-worktrees/followup-evidence/05/`.

SHA-256 of executed binaries:

| Binary | SHA-256 |
| --- | --- |
| mass-sdk-driver | `0461969bf58021d9415835d90c27cf6040a79113e6f9fd2777328a7789eb6703` |
| agent-sdk-testify | `b11e9d9381cd3577605e55ce29150fdb33b8755fdab04a330d6b2d65d952949d` |

## Executed Denominators

| Evidence class | Executed / passed | Business | Transport / consumption | Logical cleanup |
| --- | --- | --- | --- | --- |
| Admitted Codex strict simulate | 1 / 0 | Not reached; result failed | Ordinal 1 mismatch; EOF; no receipt | released=true |
| Admitted Claude strict simulate | 1 / 0 | Not reached; result failed | Ordinal 1 mismatch; EOF; no receipt | released=true |
| Synthetic Codex via actual testify simulate | 1 / 1 | Exact Ready/Done, two owned idle boundaries, final history | All 18 frames; fullConsumption=true; clientEOF=true | released=true |
| Synthetic Claude via driver CLI/stateful peer | 1 / 1 | Exact Ready/Done, two owned idle boundaries, final history | Peer complete=true/clientEOF=true; not static-recording consumption | released=true |

Strict batch: selected=2, passed=0, exit=1. Both failed cases terminated and
wrote results, summaries, runtime observations and cleanup. Neither has
`replay-peer.json`: zero full-consumption receipts, not a transport PASS.
Codex failed at Runtime startup; Claude failed at Session initialization.
Both `peer-stderr.log` files say `ordinal 1: client mismatch`.
Cleanup now reports shutdownAttempted=true, storesClosed=true, released=true
for both failures; this does not retroactively change earlier released=false
runs, or prove cleanup of other post-open failure classes.

Selected unchanged inputs under the fixed module's `tests/simulation/recordings/`:

| Provider | SDK-US-001/SDK-CASE-001 capture | raw/raw.jsonl SHA-256 |
| --- | --- | --- |
| Codex | `20261101T065559Z-6a7c186793d249cb8595d5a057c65941` | `4cef5e38ca34351a9334c4642d446611478418155bb61cbdc7a0a27da5a8eca7` |
| Claude | `20261101T065847Z-7f3b2b919eef44a091d5ce4e539406d1` | `f82aab3884357d79311055f71b084153f2e82b6643f63387af3d436bcee4d344` |

Only these two admitted continuation cases were selected. Other corpus cases
were not executed: the driver accepts case 001 with exactly two text prompts;
this is neither whole-corpus coverage nor a new alternative-capture audit.
Index admission references, adoption hashes and every adoption-listed file were
read back independently (`selected-admission-readback.json`); no new admission
or official replay was performed.

## Authored Route And Integrity

`commands.json` records the exact three CLI argument vectors. `strict.log` and
`strict-attempt/` retain the failed batch. `codex-authored.log` and
`codex-authored-attempt/` retain an actual `agent-sdk-testify simulate --materials`
execution, not merely direct `protocol mock`. Its input is the repository's
`tests/sdksimulation/testdata/codex-two-rounds.json`, with `/synthetic` replaced
by an owned absolute project and JSON compacted as documented. New synthetic
plan/raw inputs live in `authored-codex/`, outside admitted materials.
The raw hash is `ef2c5133eaf82264c0b05b142147022635538b2ea28f176c8fa6d710f5dd5203`;
its genuine runner receipt matches this hash and says nativeLaunched=false.

`authored-claude/` retains a separate driver `--request` run using absolute
Python with `-I` and repository `claude-peer.py success`. Its synthetic receipt
validates both inputs, EOF, and the fresh second submission UUID. Echoing this
UUID as user_message_uuid is authored peer behavior, not native evidence or
an SDK matcher feature. No static-recording or official PASS is inferred.
Both synthetic routes have distinct receipts, exact committed Ready/Done
history, namespace/session routing and no SessionError, independently read back.

`source-hashes-{before,after}.json` bind tracked mass-go inputs, testify sources,
selected capture files and index. Binary and authored-input before/after
inventories also agree. `validation.json` records all comparisons and assertions.
Two failed evidence-validator invocations (index entries without materials,
then an ineffective quoting correction) are retained separately; the corrected
validator passed. These did not rerun or alter simulation inputs.
`process-readback.json` uses OS process readback with the unique evidence path:
zero owned processes remain. All four cleanup records confirm stores closed;
OS absence is supplemental, not a substitute for logical release. No separate
listener-allocation assertion was made; runs use stdio peers and file stores.

## External Decisions Required

1. Authorize independently sourced/admitted Codex material with genuine MASS
   ClientInfo/capabilities, persistent-thread semantics and exact text shapes,
   plus official replay/provenance review. Do not impersonate the recorder or
   delete product persistence just to match the current capture.
2. For Claude, obtain native forwarding/linkage evidence and approve an SDK/tool
   contract that binds fresh input UUIDs to recorded IDs and linked outputs.
   A new fixed-UUID transcript alone is insufficient with the unchanged matcher.
   Alternatively explicitly accept a synthetic-only evidence goal while leaving
   official recording-driven acceptance blocked.

No external decision, recording, matcher change or native run is authorized or
performed here. These results support the authored routes and startup cleanup,
not dual-provider official acceptance or closing SDK-FU-05. Acceptance/tracker
updates and the final integration gate remain owned by SDK-FU-06/parent.
