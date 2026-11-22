# Verify massd behavior

Use the test that matches the claim you need to verify. Run commands from the repository root with the Go dependencies cached. The [Makefile](../../Makefile) owns package selection and timeouts.

## Run offline checks

| Claim | Command | Evidence |
| --- | --- | --- |
| Package behavior | `make test-unit-go` | Package-local assertions with the race detector. |
| Product behavior with controlled inputs | `make test-integration-go` | Constructed scenarios through the daemon, client, and CLI, plus direct [SDK protocol probes](../examples/sdkprobe/README.md). |
| Compatibility with captured native traffic | `make test-simulation-go` | MASS replays preserved consumer material. The gate checks the original hashes and requires `nativeLaunched=false` in the replay receipt. |

Run `make test-go` for all three layers or `make check-go` to add lint, builds, and format checks. Run `make prototype-go` for the isolated product demonstration. Its authored protocol peers exercise both drivers without native credentials or model calls.

Read the [simulation registry](../../internal/recordingtest/registry.go) for the selected cases and their matching material. The [recording indexes](../tests/testdata/simulation/) give the original capture provenance. Replay results establish behavior against those inputs. Native CLI behavior, current credentials, model behavior, and a deployed proxy need separate evidence.

## Verify a real deployment

Use an authenticated native executable, an explicit model, and fresh test-owned data and working directories. Start the daemon with a dedicated loopback listener and read back its reported URL. Identify the commit, binaries, native CLI versions, model, and scenarios you run. Keep secrets out of evidence.

Subscribe before sending work. Observe native identity and completion separately from the input receipt. For a permission or question scenario, answer a live interaction with an option the Agent actually offers. Confirm that close and resume use the same native identity. For a load scenario, use only the native history created for the check. Verify durable delivery through `session get` and `session history` before continuing on the restored connection.

After each run, stop the owned Runtime and daemon. Check the process and listener after exit. Report failures, skipped conditions, and unconfirmed cleanup with their actual evidence. See the [test and acceptance principles](../../harness/principles/testing.md) and [recording guide](recording.md) for the test contract and capture entry points.

See [historical evidence](history.md) for reports tied to earlier code and SDK pins.
