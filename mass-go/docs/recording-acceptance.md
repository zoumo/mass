# Consumer recording acceptance

This page records the offline consumer-recording gate for the Go implementation.
It does not change the [harness contracts](../../harness/index.md).

The SDK is pinned to `github.com/zoumo/agent-sdk-go
v0.0.0-20261105065348-5edf8b62e963`, source
`5edf8b62e963e59efa20383d5373a9aee278f135`, without a local `replace`.
`github.com/zoumo/agent-sdk-testify` is pinned to
`v0.0.0-20261105043421-a013c2bfa1e4` as a test-only tool. The four exports were
recorded at MASS HEAD `078a5136e435176b66c994681e31ddcb5e693782`; the then-dirty
worktree is recorded in each `index.json`.

## Status

| Evidence | Passed | Failed | Unverified |
| --- | ---: | ---: | ---: |
| Consumer replay: Codex/Claude basic and permission | 4 | 0 | 0 |
| Opt-in native recording tests under `MASS_RECORD=1` | 0 | 0 | 4 |
| Official verification | n/a | n/a | n/a for consumer provenance |
| Live Codex/Claude model, account, tools and native persistence | 0 | 0 | 2 providers |

The offline result is reproducible consumer replay. It is not a live Agent
acceptance result, and it was not obtained by invoking official verification.

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

The last run passed in 22.500 seconds. The four replay tests passed; the four
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
| `codex/basic` | `index.json` | 2182 | `2376e6df61215ded3bc79aa6349163dbe1615299ecdfff6181fcd6ce2457079d` |
| `codex/basic` | `plan.json` | 228 | `ccbe9d4413016d0320c6313df4fdb6ef07c58a835d12a2a5ff923157a7e66f5d` |
| `codex/basic` | `material.json` | 1387 | `80120d5866d82f7972fff171166da49bffb0e80e79141af6b6b9532f19ce37b9` |
| `codex/basic` | `manifest.json` | 1417 | `92059a0d5f4bae3afede4348c28fda51d2049d0962515ee0c22ffcb4495b55e5` |
| `codex/basic` | `raw/raw.jsonl` | 15206 | `09a161d8d5172fb7333e58204e2320055077f06a7d55e6001088fa6c56079425` |
| `codex/permission` | `index.json` | 6622 | `7c1d057b53ce58898cb0e5614e13ac487ca0dff8f4c6a28876dc8b7b630b6918` |
| `codex/permission` | `plan.json` | 238 | `6f16b586a0a1b9a5ee5edcfac0d17b7ee5c3c68632113bbe6bac70510cda5492` |
| `codex/permission` | `material.json` | 1397 | `dd7cdf07dcb16a3efd977ca4cce484f5ed2e159a284fb9a5a6259b51c99883b4` |
| `codex/permission` | `manifest.json` | 1437 | `0c3ae50160b3565c3e7e1ed04d32f23f2969135b69449c4ae5a64996c2ed61ba` |
| `codex/permission` | `raw/raw.jsonl` | 38373 | `d670104f3ab798a7203321e17ae900690daebb312d7815dcfade1dae8cfa08b5` |
| `claude/basic` | `index.json` | 2710 | `4314ff8ef137e79afc6234e83ed601e5de31b24e312032ef03592b5cf333d3ff` |
| `claude/basic` | `plan.json` | 266 | `4eaedb3338618c7269e7674757ec37efa018f50787ac203d61060846784c2419` |
| `claude/basic` | `material.json` | 1401 | `c35e77d128d38c083c24d3498c711c2efde15e35eb818df117e1647657378ac6` |
| `claude/basic` | `manifest.json` | 1612 | `ae1a7f6317126d203c355836a82528dad7fd65ad6ee3d093180b04d2ed554374` |
| `claude/basic` | `raw/raw.jsonl` | 24202 | `a3cb19c2fa6b6b581fedc55a5056eda386ac4a6b24f8e676c2ee07b2e116e93d` |
| `claude/permission` | `index.json` | 4365 | `400d358bdf618cb341c7ce580bcdc10d820778591bb06a28ee89944a00b1b572` |
| `claude/permission` | `plan.json` | 276 | `fe079b07c2412bca2e6877c06d977ef4ef5cab12e7fc609919d983bb864303df` |
| `claude/permission` | `material.json` | 1417 | `e54dd3bf1d86effaf5849f708d1bffabdd32287125638fece73c99ba4c558f09` |
| `claude/permission` | `manifest.json` | 1639 | `52fb9b196703ccda1eb9e143ae98cdf40e94f13492154c7aa245863afbcf11f5` |
| `claude/permission` | `raw/raw.jsonl` | 31435 | `3810685b16e4b67dec8c874ae6c0b2e5384dc0cf0609dd0456365010f7ef342c` |

Supporting raw files:

| Case | File | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `codex/basic` | `raw/manifest.json` | 854 | `4d14b81e462986e78b693646c51b473f956567ba005489cd9d12c33df1ff2eba` |
| `codex/basic` | `raw/stderr.raw` | 603 | `03e414ae12a52a7e3f2c3743de4dc8540667616a2228a0ec8d35f995a1be84bd` |
| `codex/basic` | `raw/transport.json` | 302 | `bf1804205b76f6d59dafc2ce8c6b185c336c9a6f504cf327713c315d15d8b8e0` |
| `codex/permission` | `raw/manifest.json` | 874 | `2055941c6b1ea876f2551bf8c88cdc090b859a4a5dd1e182b0a50c8ff5e30c8a` |
| `codex/permission` | `raw/stderr.raw` | 451 | `33535190e248f233830073645729bb0431e2d5d2598a84e790b42c215d8de317` |
| `codex/permission` | `raw/transport.json` | 302 | `2209c3e4aa4781cdf86173362c5a6adc1a0f42cff3320b44c27c1a877522db8b` |
| `claude/basic` | `raw/manifest.json` | 1036 | `dc4be96f801dcfb34943e09b41159ed977a367f6612c57906336c222c50ca2e9` |
| `claude/basic` | `raw/stderr.raw` | 101 | `8ae07582e9237b9ec17d84072c3fbf161956de0fe276e9adf3c9bcca9dae2ae4` |
| `claude/basic` | `raw/transport.json` | 301 | `4995b45efe27358c72a7e171f7a4b58cd2ea67e4df91d449b1ad75857d424ce3` |
| `claude/permission` | `raw/manifest.json` | 1058 | `768aa86d55d16fb4d442b9a5ef72de9c1dd0e081257a1011dbb77dd395aed3be` |
| `claude/permission` | `raw/stderr.raw` | 101 | `8ae07582e9237b9ec17d84072c3fbf161956de0fe276e9adf3c9bcca9dae2ae4` |
| `claude/permission` | `raw/transport.json` | 301 | `f891651d5d859e100fb23a015ccab66aab8cc29cbd3ea4de9e99041b1a5a5d3a` |

## Recording commands

Real recording is explicit opt-in and requires an authenticated native CLI and
cached dependencies. Run from `mass-go/`. An output directory must not already
exist; the test publishes only after two strict replays and a personal-path and
credential scan pass.

Codex basic:

```sh
MASS_RECORD=1 MASS_RECORD_CODEX=/absolute/path/to/codex MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-basic \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordCodexBasic$'
```

Codex permission:

```sh
MASS_RECORD=1 MASS_RECORD_CODEX=/absolute/path/to/codex MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-permission \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordCodexPermission$'
```

Claude basic and permission:

```sh
MASS_RECORD=1 MASS_RECORD_CLAUDE=/absolute/path/to/claude \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordClaudeBasic$'

MASS_RECORD=1 MASS_RECORD_CLAUDE=/absolute/path/to/claude \
  GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording -run '^TestRecordClaudePermission$'
```

Claude defaults to `tests/testdata/simulation/recordings/claude/<case>/`.
`MASS_RECORD_CLAUDE_BASIC_OUTPUT` or
`MASS_RECORD_CLAUDE_PERMISSION_OUTPUT` selects another absolute path.

Use the native executable directly. Codex recording runs with an isolated HOME,
`CODEX_HOME` and project, disables project instruction discovery, and uses
`model=gpt-6.1-sol`. The permission scenario uses `approval_policy=on-request`
and `sandbox_mode=read-only`, then answers the offered `allow-once` option for a
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
