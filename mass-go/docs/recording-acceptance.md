# Consumer recording acceptance

This page records the offline consumer-recording gate for the Go implementation.
It does not change the [harness contracts](../../harness/index.md).

The SDK is pinned to `github.com/zoumo/agent-sdk-go
v0.0.0-20261105065348-5edf8b62e963`, source
`5edf8b62e963e59efa20383d5373a9aee278f135`, without a local `replace`.
`github.com/zoumo/agent-sdk-testify` is pinned to
`v0.0.0-20261105165039-e4799fe3484c` as a test-only tool. The four exports were
recorded at MASS HEAD `eb3a6e8ce93e2dbd05dcdaf4b2c1daae5ac36a2e` with
`auto/deepseek-v4.1-flash-1017`; the then-dirty worktree is recorded in each
`index.json`.

## Status

| Evidence | Passed | Failed | Unverified |
| --- | ---: | ---: | ---: |
| Consumer replay: Codex/Claude basic and permission | 4 | 0 | 0 |
| Opt-in native recording tests under `MASS_RECORD=1` | 4 | 0 | 0 |
| Official verification | n/a | n/a | n/a for consumer provenance |
| Live MASS product E2E through HTTP/SSE with native providers | 0 | 0 | 2 providers |

The four recording runs invoked the real Codex and Claude CLIs, their configured
accounts and `auto/deepseek-v4.1-flash-1017`; both permission cases executed the
approved tool. The offline gate result is still reproducible consumer replay, not
a live MASS product E2E result or official verification.

## Unified gate

From the repository root:

```sh
make recording-gate-go
```

The target expands to:

```sh
cd mass-go && MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -count=1 -v -timeout=300s ./tests/recording
```

The last run passed in 28.212 seconds. The four replay tests passed; the four
opt-in recording tests skipped with `MASS_RECORD=0`; the gate, artifact-audit,
credential-audit and failure-injection tests passed.

The gate enforces:

- Exactly the four registered cases, with no missing or unregistered directories.
- `index.json` beside `material/`, never inside it.
- Matching provider, case, `consumer` provenance, export path, plan and prompts.
- Full index hashes for `plan.json`, `material.json`, `manifest.json`,
  `raw/raw.jsonl`, `raw/manifest.json`, `raw/stderr.raw` and
  `raw/transport.json`, with no extra hash entries.
- Exact material file inventory, hash bindings, saved-session existence, raw
  inventory, capture statuses and native identity.
- Successful execution index, connection, descriptor, process receipt and
  replay receipt.
- Replay receipt completeness: `fullConsumption`, `clientEOF`,
  `cleanupComplete`, matching plan/raw hashes, zero exit, empty error and
  `nativeLaunched=false`.
- Mock route identity: `executor=external-sdk`, `target=replay`,
  `provenance=consumer`.
- Rejection of personal paths, credential files, bearer values, token values,
  private keys and credential-bearing environment assignments.

Each failure is tested by mutating a copied case or execution directory; adding
or removing a file, changing a hash, changing provenance, breaking the manifest,
marking consumption incomplete, launching natively or skipping cleanup fails
the gate.

## Export location

```text
mass-go/tests/testdata/simulation/recordings/
  codex/basic/{index.json,material/}
  codex/permission/{index.json,material/}
  claude/basic/{index.json,material/}
  claude/permission/{index.json,material/}
```

`sourceSession` is relative to `/var/tmp`. The original sessions are retained
locally for audit and are private because testify stages authentication there.
They are not copied into the repository. Raw manifests may contain the owned
`/var/tmp` or `/private/var/tmp` session path; personal HOME paths and secret
values are not permitted.

## Artifact hashes

Primary exported files:

| Case | File | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `codex/basic` | `index.json` | 2073 | `1a26e663954d7ab9fd1c076fb62a3413d50c1fc2a8db77c7da0a315192ac3164` |
| `codex/basic` | `plan.json` | 246 | `4fd804eb77e7000d36866452437b6abf8c4c881e06d3c69600a11245918c4e07` |
| `codex/basic` | `material.json` | 1387 | `dec91d81cffc39aefdafd419576637b50552cc28b1295b67c1cbe54f3ffc3d9a` |
| `codex/basic` | `manifest.json` | 1417 | `2cf245909c27410c9e13e6deafb66f9b0bf0d546e919be8501a52ce1ed602524` |
| `codex/basic` | `raw/raw.jsonl` | 14306 | `c9817598e749d37ec357283ea1dd773fd1303ed79b0767e1fecb837b44e77c58` |
| `codex/permission` | `index.json` | 8455 | `cd5cb3801f9a4db550dbde6fee5f4205d8d95ae29706d522ef2cf44ddedd70cd` |
| `codex/permission` | `plan.json` | 256 | `849a1039b6b2a7574c960bc72c2b8283985207998a5f4e1ee69da22b01a294e3` |
| `codex/permission` | `material.json` | 1398 | `cb7ae6a5c4b448aa5ad3e55989b25f8e2e4dd961f13619f6889ca9df75d845aa` |
| `codex/permission` | `manifest.json` | 1439 | `68db760ae064bc8d3b1d245dd5bdd58e0a2e8e18ddbdb6bc08e04823e6f6fe16` |
| `codex/permission` | `raw/raw.jsonl` | 65724 | `ead9d25d2a2938869e503c9846d68548440f46a4fc499800e9ae7e21338bfb8d` |
| `claude/basic` | `index.json` | 2412 | `67ff3d82979cbac741d5ab26fa585b96d6046ff2a287084f91e8f530e8c963b0` |
| `claude/basic` | `plan.json` | 261 | `4f08fba508dc5eb285277932db05fa40274a3a62e57693a580ef224e23848e6a` |
| `claude/basic` | `material.json` | 1401 | `3a0112f32747ab25c591f8d714d6c9d61b3ad19d1fd3dec7dbc818638cdf9af2` |
| `claude/basic` | `manifest.json` | 1612 | `77a6556b6bf3deef5b44e39bbbb88d52a29047156a3d02935b3802535868e6d9` |
| `claude/basic` | `raw/raw.jsonl` | 24509 | `c5b09db9ce22b09a7ddf85f4bca660c7a1b6f5c357e75df4d7414858940ee717` |
| `claude/permission` | `index.json` | 4299 | `cb4d5cad178613e177a8a360bc96018b7d217e47ed344c013dfce05b3250ea62` |
| `claude/permission` | `plan.json` | 271 | `39487e428176d308da9c0063c41be278f1b96cb4370a6550e412489fae328b06` |
| `claude/permission` | `material.json` | 1417 | `7894ce6356a681ff5961c853ecd266fd0a8edcf8e4d527fda7a4c6cabb52e3a4` |
| `claude/permission` | `manifest.json` | 1639 | `dc24d6920c1bdbc466face9f749ca4b2f1d871956363495b10df9484b04cf4b6` |
| `claude/permission` | `raw/raw.jsonl` | 36748 | `a1edf9df4d0bfc463479f99be0667640be03ad0e685eca24a4861c7615584f87` |

Supporting raw files:

| Case | File | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `codex/basic` | `raw/manifest.json` | 854 | `80623394161a26ba8f90cce27ffe833691837ed334f8bbfdde8195a7e62e065b` |
| `codex/basic` | `raw/stderr.raw` | 275 | `3d16db9cda020c9f0350593e3d5b52590ad4066623ad7a863cd786927623cee3` |
| `codex/basic` | `raw/transport.json` | 302 | `710094f125ce59c477f75717ddf5cddac1a7fe2563d3a405f9216707878891dc` |
| `codex/permission` | `raw/manifest.json` | 876 | `6eb9a126109d9726f8e251f656f4179a86e7ae8c90e788f9872761d389c8225f` |
| `codex/permission` | `raw/stderr.raw` | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `codex/permission` | `raw/transport.json` | 301 | `2b1254054240db043bdfe372ada0d9ad07ae6af9b9c477a5e240469fec61a88c` |
| `claude/basic` | `raw/manifest.json` | 1036 | `6b9ce51e63dff1ecc122a9ae19d91be99963f1587788ed1c484fddb34f3caa33` |
| `claude/basic` | `raw/stderr.raw` | 100 | `a3e5c0ea281d47276957c8ac092518c30a2f8b1b63a3a9c9da6cb668ee9a6906` |
| `claude/basic` | `raw/transport.json` | 301 | `c62ade653f8bda509b05391361eebd3013ea9129fd0de6611610590ea83993d1` |
| `claude/permission` | `raw/manifest.json` | 1058 | `ab18c64de9a3a9981b9f96fd182267c6ef97f7d31191c73e85fc88eba9173484` |
| `claude/permission` | `raw/stderr.raw` | 100 | `a3e5c0ea281d47276957c8ac092518c30a2f8b1b63a3a9c9da6cb668ee9a6906` |
| `claude/permission` | `raw/transport.json` | 301 | `e62e854da8080ac3037d57fe29df9a33decdf04088fa07f26debe5d580f0cf21` |

## Recording commands

Real recording is explicit opt-in and requires an authenticated native CLI and
cached dependencies. Run from `mass-go/`. An output directory must not already
exist; the test publishes only after two strict replays and a personal-path and
credential scan pass.

Codex basic:

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CODEX=/absolute/path/to/codex MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-basic \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordCodexBasic$'
```

Codex permission:

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CODEX=/absolute/path/to/codex MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-permission \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordCodexPermission$'
```

Claude basic and permission:

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CLAUDE=/absolute/path/to/claude \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordClaudeBasic$'

MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CLAUDE=/absolute/path/to/claude \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordClaudePermission$'
```

Claude defaults to `tests/testdata/simulation/recordings/claude/<case>/`.
`MASS_RECORD_CLAUDE_BASIC_OUTPUT` or
`MASS_RECORD_CLAUDE_PERMISSION_OUTPUT` selects another absolute path.

Use the native executable directly. `MASS_RECORD_MODEL` selects the same model
slug for both providers; without it, Codex uses `gpt-6.1-sol` and Claude uses
the model from `CLAUDE_CONFIG_DIR`. Recording runs with an isolated HOME,
`CODEX_HOME` or `CLAUDE_CONFIG_DIR`, and project; project instruction discovery
is disabled. The Codex permission scenario uses `approval_policy=on-request` and
`sandbox_mode=read-only`, then answers the offered `allow-once` option for a
harmless project-local write. Claude permission uses inline
`permissions.ask: ["Bash"]` and answers the offered `allow-once` option through
the public HTTP answer API. If the expected interaction is absent, the case is
unverified and no passing export is published.

## Claim boundary

- Consumer captures are valid replay inputs, not official captures. Official
  verification requires `official` or `builtin` provenance and is not applicable.
- Replay launches the testify external-sdk route, not a native Agent. The route
  and receipt checks prove `target=replay`, `provenance=consumer` and
  `nativeLaunched=false`.
- Replay proves MASS policy and public API behavior for these four recorded
  exchanges. It does not prove authentication, current model behavior, native
  tool execution, native history durability or provider-wide compatibility.
- Claude input receipts prove a complete local write; they do not invent native
  acceptance. Permission answers use the actual offered option ID and record
  `nativeAccepted=null`.
- Do not report the offline gate as live Codex/Claude or model acceptance. Live
  Agent E2E remains unverified until the separately authorized procedure in the
  [acceptance map](acceptance.md#separately-authorized-native--real-model-procedure)
  is actually executed.
