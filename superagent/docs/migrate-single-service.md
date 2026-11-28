# Migrate to the single-service release

Rehearse the upgrade and rollback with isolated copies before you schedule
the cutover. Use matching releases of Superagent and the standalone
AntCode Trigger. This procedure expects Core schema 28, Trigger schema 3,
and SQLite metadata for embedded MASS.

Use the existing Core data directory if it has no SessionBindings that
refer to `/v1/mcp`. If it contains those bindings, initialize a new Core
data directory and keep the previous deployment snapshot for Core record
and binding history. New Core rejects a persisted Router with a different
MCP endpoint. If you retain the MASS data with a fresh Core, assign the
Core a new `sessions.namespace` so it creates distinct role Sessions.

## Prepare and back up

1. Record the Core origin, Session workspaces, Runtime names, and current
   Router, Topic, and Issue binding IDs. Record a Trigger receipt, MASS
   Session history, and each Trigger's checkpoint and queue status.
2. Pause incoming traffic. Stop standalone Triggers, then stop Compose
   or standalone Core. If you use standalone MASS, stop it after Core
   when taking the backup. Verify that each process has exited. A clean
   MASS shutdown records native owner release where MASS can confirm it.
3. Compare outstanding dispatches and Outbox sends with original Session
   history, Core receipts, and channel evidence. Resolve uncertain effects
   before transferring input. If you start a fresh Core, reconcile any
   Trigger item already accepted by the previous Core before replay.
4. Back up Core's entire `dataDir`, the entire MASS data directory, and
   each Trigger database. Preserve DWS send evidence and reaction state.
   Save the old binaries, configuration, credentials, and deployment
   routes with the backup. If you copy SQLite files, keep any adjacent
   journal files with their databases.

## Start the new Core and verify retained data

1. Select the Core `dataDir`. For an in-place cutover, keep its absolute
   path. Existing role Session specs reference files under
   `<dataDir>/mcp-files`.
2. For embedded MASS, move the old `massd.embedded.dataDir` to
   `<dataDir>/massd` and remove `massd` from Core YAML. For standalone MASS,
   keep its existing data and configured `massd.endpoint` and
   `massd.tokenFile`. Start standalone MASS before Core. If the old
   embedded instance uses file metadata, run MASS separately with that
   backend or migrate its metadata to SQLite before embedding it.
3. Install the old `accessToken.admin` and `accessToken.triggers` at
   `<dataDir>/credentials/admin.token` and
   `<dataDir>/credentials/triggers.token`. Use directory mode `0700` and
   file mode `0600`. Keep the two tokens distinct. If either token was
   absent, Core creates it on startup. Remove `accessToken` from Core YAML.
   Reconfigure embedded MASS clients to use the admin token. Standalone
   MASS keeps its own credential.
4. Point callers and deployment routes at the replacement paths. Start
   Core with `superagent serve --config core.yaml`. You can start Compose
   with only the `core` section while you verify the service. Keep Trigger
   traffic paused and omit Watchdog during the rehearsal.
5. Read the ready record and check `/readyz` and `/status`. Read back an
   existing Trigger receipt with the original `sourceMessageId`, Source,
   and Account ID if you retained the Core data. Use the admin token
   for Operator commands and MASS RPC. Compare the existing Runtime
   UID, Session IDs, and history. Read older role Session history
   through MASS by Session ID.

| Previous path | Replacement path |
| --- | --- |
| `/superagent/` and `/superagent/<page>` | `/` and `/<page>` |
| `/superagent/assets/*` | `/assets/*` |
| `/v1/core/*` | `/apis/core/v1/*` |
| `/v1/mcp` | `/apis/core/v1/mcp` |
| `/v1/telemetry/{traces,metrics,performance}` | `/apis/core/v1/telemetry/{traces,metrics,performance}` |
| Embedded `/massd/{jsonrpc,livez,readyz}` | `/apis/massd/{jsonrpc,livez,readyz}` |
| `/livez`, `/readyz`, and `/status` | `/livez`, `/readyz`, and `/status` |

Web and the APIs use the Core origin. Keep `externalURL` reachable from
the Session host. Update the reverse proxy or TLS endpoint if you use one.
Trigger configuration takes a Core origin. The Trigger binary adds the API
path.

New Router, Topic, and Issue Sessions use `/apis/core/v1/mcp`. Read their
specs through MASS and confirm their role tools work before reopening
traffic. Keep former role Sessions offline. Their saved specs still point
to the previous MCP endpoint. For unsettled input on a binding in its
owning Core instance, use
[Operator reconciliation](../API.md#operator-reconciliation).

## Resume Triggers

Restart with the full Compose configuration or start the standalone
Triggers and Watchdog. For an in-place cutover, keep the original database
paths, Source, Account, and instance IDs. For a fresh Core, start reply
adapters with fresh state after you reconcile the old Outbox. Set
standalone Triggers' `coreTokenEnv` to the Core Trigger token through your
deployment environment. Compose supplies that token to its children.
Check status, a Trigger Message receipt, and the Outbox for reply-enabled
adapters before reopening traffic.

## Roll back

1. Pause traffic and stop the new Triggers, Core, and any standalone
   MASS instance in the restore set. Verify that each owner has exited.
2. Save the upgraded state separately. Compare Messages, checkpoints,
   accepted inputs, and any external sends since the backup. Resolve
   which effects you need to reconcile before replay.
3. Restore the pre-cutover Core, MASS, and Trigger snapshots together.
   Put the files back at their former paths. Restore the old YAML,
   credentials, deployment routes, and binaries. New binding specs
   contain the new MCP URL, so restore the Core snapshot as well as the
   old binary.
4. Start standalone MASS first if used, then Core. Verify the old binding
   IDs, Session history, and receipts. Start the old Triggers and verify
   their status and queue. Reopen intake after confirming the restored
   owners and any required reconciliation.
