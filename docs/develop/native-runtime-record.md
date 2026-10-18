# Native runtime implementation

The Rust production adapter lives in `mass-rs/codex`. Public lifecycle, admission, cancellation and evidence rules are defined in the [native contract](../design/native-runtime-connectors.md). The provider-neutral boundary lives in `mass-rs/runtime`; HTTP, sessions, events and journal depend on that boundary rather than native SDK types.

A configured local runtime lazily owns one Codex 0.160.1 app-server. The fixed official client owns the private Unix WebSocket transport, handshake and RPC routing. The adapter owns native identity, command handoff, observations, approval handles and process cleanup. Codex Remote uses the same adapter stack with a separately owned connection and no authority to stop the remote service.

Native acknowledgements, user-message recording, durable commit and foreground completion are distinct observations. Cancellation cannot retract a command already handed to the SDK. Approval checks occur before SDK handoff; they are not socket-write revocation. Unknown notifications are diagnostic; malformed known facts fail the attributed attachment or shared service according to the contract.

The official client and reversible repository patch remain under `mass-rs/vendor/codex-app-server-client`, with upstream revision, pristine hashes, licenses and provenance checks. See the [client guide](codex-client.md) and [bounded delivery rules](../design/native-runtime-connectors.md#native-ticket-06-bounded-core-delivery-and-shared-transport-pressure).

The Rust product has no production Claude/ACP adapter. The separate [Go experiment](../../mass-go/README.md) uses the public Go SDK and synthetic CLI peers; it does not replace the Rust lifecycle or persistence contracts.

Use [offline integration tests](../testing.md) for deterministic verification. Real Codex/model acceptance requires an explicitly reviewed executable and credentials supplied outside the repository. Raw captures, local checkout locations and internal reference implementations are not distributed with this source.
