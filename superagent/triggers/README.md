# Run a Superagent Trigger

DWS collects DingTalk Messages and sends replies through the Core Outbox.
GitHub collects Issue and PR changes from GitHub.com notifications. Build both
with `make build-superagent` from the repository root. Use
[`superagent compose`](../README.md#run-the-stack-with-compose) to manage Core
and the Triggers together.

## Bind a Trigger to Core

For standalone Triggers, use an absolute `databasePath` on local persistent
storage for each instance. In Compose, resolve paths relative to the stack
YAML file. Preserve the database, source, account, and instance ID across
restarts. The Core `sources` entry must match all three identities. Supply the
same token from its `tokenFile` through the environment variable named by
`coreTokenEnv`. Assign a separate token to each instance.

For DWS replies, select one sender per account. If more than one Core source
credential has an instance ID for the account, set `outboxConsumer: true` on
the designated sender. Other instances can keep collecting Messages. See the
[Outbox contract](../API.md#trigger-outbox).

### Connect to Core on another host

Use an HTTPS origin in `coreEndpoint`. For explicit HTTP on a trusted network,
set `allowInsecureCoreHTTP: true`. That connection carries the Core token and
Message contents in clear text.

For direct access, use the numeric address and port that receive the request.
For HTTPS through a reverse proxy, set upstream `Host` to that Core address
and port. Trigger requests ignore redirect responses and proxy environment
settings.

## Configure the DWS Trigger

1. Copy [the DWS example](config.example.yaml) to your run directory. Configure
   the local `dws` executable and its existing `corpId:accountId` profile. Set
   `accountId` to the profile's account ID and `selfOpenDingtalkId` to its native
   openDingTalk ID.
2. Enable the scopes you need. `direct` collects messages from the other
   participant. `selfChat` uses the exact native self conversation ID. Each
   group uses its native conversation ID. Use `replyEnabled` for any group
   that should receive Outbox replies. Authorize non-interactive DWS reply
   confirmation before starting a sender.
3. Configure at least one match rule per enabled scope. Rules apply before the
   optional group prompt is appended. Quote a prefix with trailing whitespace.
4. Check the configuration and start the Trigger. Query the local status origin
   printed by `serve`. `consumerReady` becomes true after every configured
   subscription reports ready.

   ```sh
   bin/superagent triggers dws check --config /absolute/run/dws.yaml
   bin/superagent triggers dws serve --config /absolute/run/dws.yaml
   bin/superagent triggers dws status --endpoint http://127.0.0.1:24371
   ```

DWS provides native Message and conversation IDs. The Trigger preserves them
and uses the native sender ID as `actor`. A source timestamp requires an
absolute event time. Quotes and forwarded Messages are reference material.
Media is not fetched. See the [DWS Message adapter](dws/message.go) for the
field mapping. Matched self-chat input lets Core check for
[outbound echoes](../API.md#dws-outbound-echoes).

### Recover DWS intake and replies

Check the Trigger's local status for `stopReason`, backlog, and
`permanentFailures`. Core admission settles a queued Message only after a
complete accepted or duplicate receipt. The Trigger retries uncertain intake
with its original identity. At capacity, it stops subscription intake and
attempts to drain the queue. Restart drains existing pending items before
subscribing again. Recovery starts at the SQLite queue commit. Messages that
arrive during a subscription interruption or before that commit depend on DWS
redelivery.

A DWS sender writes the task ID to the Outbox before polling it. A replacement
polls that same task. If a result write loses its response, the sender retries
the same write. A missing or expired native task can lead to a new send. Check
Outbox receipts before treating an uncertain send as a failure. An interrupted
send may have reached DWS before Core saved its ID or outcome.

For optional message reactions, configure two reusable text emotion templates
for the same DWS profile. Keep `feedback.statePath` on persistent storage,
separate from the queue. Feedback adds `thinking` on admission and replaces it
with `done` after the first confirmed reply in that conversation.

On shutdown, the Trigger stops its owned DWS consumers and attempts to admit
pending Messages and save known reply results. If it reports an unconfirmed
consumer exit, check that consumer through DWS before restarting.

## Follow GitHub account notifications

1. Copy [the GitHub example](github.config.example.yaml) to your run directory.
   Choose repository filters or omit `repositories` for all accessible
   repositories. Remove legacy `match` settings. A repository prompt affects
   newly queued Messages.
2. Supply a classic personal access token with `notifications` or `repo` scope
   through `githubTokenEnv`. Private repositories and their Actions readback
   require the corresponding `repo` access and any organization authorization.
3. Run `check` and `serve`. `check` reads the Notifications API. Query the local
   status origin printed by `serve` for successful polls, failed readbacks, PR
   tracking, backlog, and Core receipts.

   ```sh
   bin/superagent triggers github check --config /absolute/run/github.yaml
   bin/superagent triggers github serve --config /absolute/run/github.yaml
   bin/superagent triggers github status --endpoint http://127.0.0.1:24372
   ```

A fresh database considers notification versions from its startup boundary.
Restart with the same database to retain checkpoints and pending deliveries.
One notification can yield several changes after the Trigger reads the object
and discussion. It can recover only the history GitHub exposes. It leaves read
status and the inbox unchanged.

The GitHub Trigger uses `https://github.com/{owner}/{repo}/pull/{number}` as
its PR Thread key. It lowercases the owner and repository. In `pullRequests.add`,
submit that same URL without a query, fragment, or trailing slash. A PR message
includes the PR URL and any comment permalink in its text block. GitHub Issue
messages retain their existing `github:` keys. Configure the Core source's
`threadKeys` or `threadKeyPrefixes` to grant only the GitHub resources it should
write.

A qualifying notification for
an open PR starts Actions tracking. The first Actions read establishes a
baseline without a Message. Completion or a changed conclusion can yield a
Message. Tracking stops on merge or close. A reopened PR needs a qualifying
notification to resume tracking. See the [PR routing contract](../API.md#pull-request-registration-and-subscriptions).

## Replace deployed Triggers

1. Record each Trigger's source, account, instance, and persistent paths.
   Check status, pending Messages, permanent failures, and DWS Outbox tasks.
2. Stop old Triggers gracefully. Verify the old DWS sender has exited before
   starting a replacement for the account.
3. Back up the stopped databases, DWS feedback state if enabled, and the old
   configuration. Keep the identities and use state compatible with the new
   release. Import legacy GitHub v2 JSON state into SQLite before switching.
4. Run each standalone `check`, or start Compose to run its preflight. Query
   each Trigger's status, verify a new receipt in Core, and check that pending
   Messages resume delivery.

For rollback, stop the replacement and verify the DWS sender exited. Reconcile
queued Messages and DWS send results produced during the cutover. Restart the
previous version with compatible state that preserves the reconciled progress.

Run `make test-superagent` to exercise Core and the Triggers with local channel
fixtures. Use `bin/superagent triggers --help` to discover the command groups.
