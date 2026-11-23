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

Use one DWS adapter per Core source and account. The same adapter collects all
configured conversations and consumes their Outbox. Keep the adapter's database
and its adjacent `<databasePath>.dws-sends` directory together on persistent
storage. The local owner lock rejects another process under the same OS user
with the same Core origin, source, and account, even when the profile or state
path differs. Deploy one owner across hosts, OS users, and Core endpoint aliases.
The lock operates on one host under one OS user and compares the configured
origins. If the account has additional Core credentials, configure the adapter's
credential as `outboxConsumer: true`.

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

The DWS adapter derives `threadId` from the source and native conversation ID.
Grant that prefix in the Core source's `threadIdPrefixes`, for example `dws:`.
The adapter includes the native sender and available source time in the text
content block. The adapter saves native reply and quote identities, then sends
Core an opaque reply target and any confirmed Outbox reference.

### Recover DWS intake and replies

Check the Trigger's local status for `stopReason`, backlog, and
`permanentFailures`. Core admission settles a queued Message only after a
complete accepted or duplicate receipt. The Trigger retries uncertain intake
with its original identity. At capacity, it stops subscription intake and
attempts to drain the queue. On restart, the adapter begins processing durable input before subscribing.
It can resume subscriptions while unresolved matches are held. Recovery starts at the SQLite queue commit. Messages that
arrive during a subscription interruption or before that commit depend on DWS
redelivery.

The adapter saves each send intent, native task, and result before updating Core.
After a restart, it polls the saved task or resubmits the saved result. A missing
native task permits another attempt after the Core retry deadline. The adapter
holds potential echoes and quoted replies while their native identity is unclear.
Unrelated input can still reach Core. `held` and `outboundEchoes` report local
matching activity.

Stop the adapter before inspecting or changing its state. Inspect evidence to
find a native input ID or send attempt ID. After checking native DWS history, use
`release-input` to admit one verified input. Use `dismiss-send` if you have
verified that an unknown send did not deliver. Both commands retain the reason.

```sh
bin/superagent triggers dws inspect-evidence --config /absolute/run/dws.yaml
bin/superagent triggers dws release-input <native-id> --reason "<reason>" --config /absolute/run/dws.yaml
bin/superagent triggers dws dismiss-send <attempt-id> --reason "<reason>" --config /absolute/run/dws.yaml
```

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
its PR Thread ID. It lowercases the owner and repository. In `pullRequests.add`,
submit that same URL without a query, fragment, or trailing slash. A PR message
starts with a text block for the body and source facts. It then includes the PR
URL and any distinct observation URL, such as a comment permalink or Actions
run page, in that order as `resource_link` blocks with no `mimeType`. GitHub
Issue messages retain their existing `github:` IDs. Configure the Core source's
`threadIds` or `threadIdPrefixes` to grant only the GitHub resources it should
write.

A qualifying notification for
an open PR starts Actions tracking. The first Actions read establishes a
baseline without a Message. Completion or a changed conclusion can yield a
Message. Tracking stops on merge or close. A reopened PR needs a qualifying
notification to resume tracking. See the [PR routing contract](../API.md#pull-request-registration-and-subscriptions).

## Replace deployed Triggers

When switching an existing installation to `threadId`, stop the old Triggers
and configure a fresh `databasePath` for each adapter. For DWS,
choose a path with a fresh adjacent `.dws-sends` directory and use a fresh
`feedback.statePath` if reactions are enabled. Keep the former state paths for
reference. The Triggers require SQLite schema 3. On subsequent restarts, use the same paths to recover queued Messages,
GitHub checkpoints, and DWS send evidence.

Run `make test-superagent` to exercise Core and the Triggers with local channel
fixtures. Use `bin/superagent triggers --help` to discover the command groups.
