# Deploy the single-service release

Use matching Core and Trigger binaries. Prepare a new Core data directory
and new Trigger database paths. Keep the previous deployment's files and
MASS Session history available for rollback.

## Start the new stack

1. Pause intake. Confirm the outcome of in-flight delivery and Outbox sends
   before stopping the old Triggers and Core. Keep their configuration and
   data available, including any SQLite journals.
2. Copy the new [Compose example](../compose.example.yaml). Keep the new
   Core and Trigger data paths separate from the previous deployment. Core
   creates its embedded MASS data under `<dataDir>/massd` and its two
   credentials under `<dataDir>/credentials`. To use a standalone MASS,
   configure its endpoint and choose a new `sessions.namespace` for Core.
3. Start Compose. Read its ready record, then check `/readyz`, `/status` and the
   Web homepage at `/console/`. In embedded mode, use `credentials/admin.token`
   to verify MASS RPC and Core management commands.
4. Point ingress and API clients at the Core listener. Forward `/` so Core
   can redirect it to `/console/`. Route these paths to the same listener.

| Previous path | Replacement path |
| --- | --- |
| `/superagent/` and `/superagent/<page>` | `/console/` and `/console/<page>` |
| `/superagent/assets/*` | `/console/assets/*` |
| `/v1/core/*` | `/apis/core/v1/*` |
| `/v1/mcp` | `/apis/core/v1/mcp` |
| `/v1/telemetry/{traces,metrics,performance}` | `/apis/core/v1/telemetry/{traces,metrics,performance}` |
| Embedded `/massd/{jsonrpc,livez,readyz}` | `/apis/massd/{jsonrpc,livez,readyz}` |
| `/livez`, `/readyz`, and `/status` | `/livez`, `/readyz`, and `/status` |

5. Set each standalone Trigger's `coreEndpoint` to the new Core origin and
   supply `credentials/triggers.token` through its `coreTokenEnv`. Start the
   matching Trigger version and check its status. Read back a Trigger
   receipt before reopening intake.

## Roll back

Pause intake and stop the new Triggers and Core. Keep their data for
reconciliation. Restore client addresses and start the previous binaries
with their original configuration, credentials and data directories.
Verify the previous owners, checkpoints and outstanding deliveries before
reopening intake.
