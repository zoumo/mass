# Current-pin Codex and Claude consumer recording acceptance

This page records the current Codex and Claude recording procedure and evidence
boundary. It does not change the harness contracts.

The Codex suite was captured on November 11, 2026 while the runtime SDK was
pinned to `v0.0.0-20261111031457-4959190bc518` and testify to
`v0.1.3-0.20261111025559-1f8db14a50e1`, without a local replacement. The Claude
suite was re-recorded on November 13, 2026 with the current runtime SDK
`v0.0.0-20261113030733-09d4ddf1ff20` and testify `v0.2.1`. Each suite retains
the versions captured in its own index.
Codex CLI `0.166.0` recorded basic and permission through the public MASS
HTTP/SSE and history APIs. Both exports passed two strict replays before
publication. The explicit SessionSpec.Config model was
`auto/deepseek-v4.1-flash-1017`; native thread responses returned that ID.
Magpie CLI usage readback bound the same native session IDs to upstream
`DeepSeek-V4.1-Flash-1017`, status `200`, and `swapped=false`.

Permission used the actual native `allow-once` option, observed interaction
closure, a linked successful tool result with exit code `0`, a project-local
`permission-proof.txt` containing `mass-approved`, and a completed native turn.
The current SDK exposes command input on the linked public ToolCall; permission
request content is optional. The recorder verifies the toolCallId and exact
write command through public history before answering. It does not invent
content, a completion event, or native acceptance of the answer.

## Explicit material selection

`internal/recordingtest/registry.go` selects each active provider/case, suite,
material ID, SDK version, testify version, and project:

| Provider/case | Suite | Material ID | Current-pin replay |
| --- | --- | --- | --- |
| Codex basic | `recordings-4959190bc518` | `50194530555049c8e6a258fa3b85ebb5` | passed |
| Codex permission | `recordings-4959190bc518` | `a172abf87285e44f5d82f74a258d9fff` | passed |
| Claude basic | `recordings-09d4ddf1ff20` | `5cf2bc799cde1a0f60ed6706ca36ef15` | passed |
| Claude permission | `recordings-09d4ddf1ff20` | `21e27325e989edaf20923eade3fada20` | passed |

The active suites are under
`tests/testdata/simulation/recordings-4959190bc518/codex/<case>/` and
`tests/testdata/simulation/recordings-09d4ddf1ff20/claude/<case>/`.
All four historical cases remain in `tests/testdata/simulation/recordings/`
with their original bytes, indexes, hashes and provenance. Each suite has an
exact provider/case inventory. Audits reject missing or extra suites, providers,
cases and files, wrong active selections, material identity, versions, hashes,
provenance, model, incomplete execution and cleanup evidence.

Every new index requires hashes for `plan.json`, `material.json`,
`manifest.json`, `raw/raw.jsonl`, `raw/manifest.json`, `raw/stderr.raw` and
`raw/transport.json`. Historical Codex indexes retain their four primary hashes;
their complete raw inventories remain independently audited.

The new Claude cases use CLI `2.1.293`. MASS opens with `--await-claim`, does not
pass `--session-id`, and adopts the identity returned by `claim_session`. Each
export passed two strict replays before publication. Full simulation passes;
real resume/load acceptance is not claimed. See the
[historical snapshot](recording-acceptance.md) for earlier evidence.

## Focused offline acceptance

Run from `mass-go/`. First confirm that selection is nonempty:

```sh
MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -list '^(Test(Codex|Claude)(Basic|Permission)Replay|TestRecordingRejectsIncompleteExecution|TestRecordingGate|TestRecordingAuditRejects.*|TestRecordingRejectsInvalidMaterial)$' ./tests/simulation
```

Then run the same focused selection with race checks:

```sh
MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -race -count=1 -timeout=600s ./tests/simulation \
  -run '^(Test(Codex|Claude)(Basic|Permission)Replay|TestRecordingRejectsIncompleteExecution|TestRecordingGate|TestRecordingAuditRejects.*|TestRecordingRejectsInvalidMaterial)$'
```

These commands use external-sdk/replay/consumer materials with
`nativeLaunched=false`; they do not launch a native Agent or call a model.
`TestRecordingRejectsIncompleteExecution` completes and validates a legal new
Codex basic replay before mutating execution, consumption and cleanup evidence.
Replay proves the captured exchange, not current authentication, arbitrary model
behavior, native history durability, or provider-wide compatibility.

## Explicit real recording

Real recording is separately opt-in. Use the actual native executable, explicit
DeepSeek model, and a new absolute output directory. Run each case separately:

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CODEX=/absolute/path/to/native/codex \
  MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-basic \
  MASS_RECORD_DIAGNOSTICS=/var/tmp/new-mass-codex-basic-diagnosis \
  GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -tags=e2e -count=1 -v -timeout=300s ./tests/e2e -run '^TestRecordCodexBasic$'
```

For permission, use unique permission output/diagnostics paths and select
`^TestRecordCodexPermission$`. An existing output directory is never overwritten.
Recording flows have a two-minute budget; each replay has a 60-second budget.
Failures retain source sessions, facts when completed, protocol stages and
cleanup diagnostics. A failed recording is never published as a passing export.

The verified Magpie route additionally used this process-only variable:

```sh
export MASS_RECORD_CODEX_CONFIG_JSON='["model_provider=\"magpie\"","model_providers.magpie={name=\"magpie\",base_url=\"http://127.0.0.1:3425/v1\",wire_api=\"responses\",requires_openai_auth=true}"]'
```

The JSON array accepts native provider selection/configuration overrides only.
The model itself still goes through public SessionSpec.Config. The overrides
do not change personal configuration or approve tools. Credentials stay in
testify's private session staging and are excluded from published materials.
Permission alone uses `approval_policy=on-request` and `sandbox_mode=read-only`.
There is no global auto-approval.

Recheck CLI executable/version/hash, authentication, provider route and the
actual served DeepSeek model before a new formal recording. A default ChatGPT
account rejected this DeepSeek ID during the retained diagnostic attempt;
the verified proxy route was required. A non-DeepSeek smoke is historical
diagnostic evidence and cannot establish readiness for this recording suite.
