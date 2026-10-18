# Offline public Runtime consumer

From the Go module directory, with Go 1.26.9 available on PATH:

```sh
mise exec go@1.26.9 -- go run ./examples/offline
```

This separate `main` package imports only the public Runtime and catalog. It builds
the pinned public SDK's two mock CLI commands into a temporary directory, writes
strict synthetic scenarios, replaces each mock child's environment, and removes
only its own temporary files. It accepts no real CLI path and has no fallback to
an installed Agent. Building requires the normal public module dependencies to
be available; no model, credential or account discovery is involved.

The example queries system types, registers named configurations, activates each
Runtime, creates native identity, continues Claude's explicit pending input,
submits Codex text, reads and JSON-round-trips typed sequenced events, queries
managed state and inspects close/stop evidence. The two protocols expose the same
common interfaces. Their native identity and submission evidence remain different.

Both close lines must say `close=close_unknown nativeRelease=unsupported`.
Local cleanup cannot turn native release into success. Stop uses explicit abort;
`incompleteFacts=true` is expected because upstream completeness is unproven.
Random operation/epoch values are not printed. More extensive concurrent-session,
recovery, cancellation, question/permission, child/background and bounded-delivery
scenarios run in the module's public-interface test suites.

See the [usage guide](../../README.md), [language-independent contract](../../docs/runtime-contract.md)
and [event schema](../../docs/events.md) for supported mappings and remaining gaps.
Passing this example verifies synthetic SDK transport consumption, not a live CLI,
external server, model or production deployment.
