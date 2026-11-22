# Record or replay Agent sessions

Run the commands from the repository root. `make test-simulation-go` replays the selected exports through MASS and runs the artifact and receipt checks. Use the [simulation registry](../../internal/recordingtest/registry.go) to find the inputs.

## Check the native executable

Use the absolute path to an authenticated native executable and a fresh absolute output path. Select an available model explicitly. The example expects `MASS_RECORD_MODEL` to contain that model.

```sh
go run -mod=readonly ./cmd/mass-record-diagnose \
  --provider codex --executable /absolute/path/to/codex \
  --model "$MASS_RECORD_MODEL" --output /var/tmp/mass-codex-preflight
```

Read `diagnosis.json` for the preflight result. `preflight_only` confirms metadata and redacted authentication checks. Add `--smoke` with a new output path if you intend to make a real model call. `ready` requires native completion and an assistant message. Use `--help` for selectors and [the diagnosis command](../../cmd/mass-record-diagnose/subcommand/root.go) for option definitions.

## Capture a new recording

Supply credentials through the recording process environment or the native CLI's supported sign-in mechanism. Testify isolates the native home. If Claude auth requires `ANTHROPIC_BASE_URL` or a token configured in user settings, supply the needed values to the recording environment. Treat private source sessions and diagnostics as sensitive.

Set `MASS_RECORD_MODEL` to the model to record and select an absolute binary path. Use a fresh output directory outside the checkout. For example, run the Codex basic case.

```sh
MASS_RECORD=1 \
  MASS_RECORD_CODEX=/absolute/path/to/codex \
  MASS_RECORD_OUTPUT=/var/tmp/mass-new-codex-basic \
  go test -mod=readonly -tags=e2e -count=1 -v ./massd/tests/e2e -run '^TestRecordCodexBasic$'
```

For Claude, use `MASS_RECORD_CLAUDE` and the appropriate `MASS_RECORD_CLAUDE_BASIC_OUTPUT` or `MASS_RECORD_CLAUDE_PERMISSION_OUTPUT` with a fresh path. Choose the basic or permission test in the [E2E entry point](../tests/e2e/recording_test.go). These tests opt in to real Agent and model calls via `MASS_RECORD=1` and `-tags=e2e`. A permission recording requires an actual native interaction and verifies its result.

Read the export index for recording provenance and inspect the artifact before sharing it. The recorder publishes only after strict replays and audit checks. If recording stops early, inspect `diagnosis.json` and the logs in the test's private scratch directory. Set `MASS_RECORD_DIAGNOSTICS` to choose a separate evidence directory. Preserve the previous export while diagnosing a failed recording.
