# Triggers for Superagent

Superagent includes two Trigger commands. The DWS Trigger collects DingTalk messages and sends replies from the Core Outbox. The GitHub Trigger collects changes in GitHub.com repositories.

For one configuration and one supervisor, use
[`superagent compose`](../README.md#run-the-stack-with-compose). The
standalone commands below use `coreEndpoint` and absolute paths for Trigger
state. In Compose, relative paths start at the stack YAML's directory.

## Configure the DWS Trigger

Build with `make build-superagent` from the repository root. Copy
`config.example.yaml` outside the checkout and fill in the native IDs. Use
`corpId:accountId` from your DWS profile and set `accountId` to the same account.
Set `selfOpenDingtalkId` to that account's native openDingTalk ID. Use a stable
`source` for each account and keep it consistent across collection instances
for that account.

- Enable `direct` to listen to all conversations with other people. Only incoming
  messages from the other person are eligible. The Trigger discards messages
  sent by the account owner before it evaluates any rule.
- Enable `selfChat` to listen to your own messages in the exact native
  `selfChat.conversationId`. Use its own `match` rules, even when `direct` is enabled.
- Add each group to `groups` by its exact native `conversationId`. Each group
  has its own `match` rules and optional `prompt` of up to 4096 UTF-8 bytes.
  The Trigger appends the prompt in a separate block after a match. Group
  Messages enter Core with `addressed=group` and no reply channel.

Each enabled scope requires at least one rule. A rule specifies one of
`prefix`, `contains`, or `regex`. The Trigger accepts the original text if
any rule matches. Matching is case-sensitive and preserves whitespace. Quote
a prefix that ends in a space. Regular expressions use Go syntax.

Set the corresponding Core `sources` entry to the same `source`, `accountId`
and `instanceId`. Each instance needs its own Core token. For each account, run
one active Outbox consumer. When the account has more than one instance-bound
credential, set `outboxConsumer: true` on the sending instance in Core. The other
instances may continue collecting messages. Stop the old sender before
starting its replacement. Use the same instance ID across restarts. It allows up to 128 ASCII letters, digits, periods, underscores,
colons and hyphens, starting with a letter or digit.

Reference a process environment variable with `coreTokenEnv`. Resolve the Core
channel token through the deployment's credential mechanism before starting;
Keep token values in the credential mechanism. The executable reads only
that environment value, removes it from every DWS subprocess environment, and
never returns it in status or logs. DWS owns its own profile/authentication
files. The adapter does not read or copy them.

```
bin/superagent triggers dws check --config /absolute/path/trigger.yaml
bin/superagent triggers dws serve --config /absolute/path/trigger.yaml
bin/superagent triggers dws status --endpoint http://127.0.0.1:24371
```

`check` validates the YAML configuration, the Core token, and the local DWS
command contracts. `serve` runs the same checks before starting its consumers.
The Trigger uses one all-direct DWS subscription when `direct` is enabled,
even if `selfChat` is enabled too. A self-chat-only configuration subscribes to
the account owner. Each configured group gets its own subscription. The local
status endpoint defaults to an ephemeral numeric loopback port and appears in
the first JSON line from `serve`. Set an explicit loopback port for operational
status queries. Configuration rejects unknown fields.

### Connect to Core on another host

Both DWS and GitHub accept an HTTPS origin in `coreEndpoint`. Use
`http://127.0.0.1:24370` for a local Core. For direct HTTP over a trusted
network, set `allowInsecureCoreHTTP` to `true` in the Trigger configuration.
HTTP sends the Core token and message contents in clear text.

When connecting to Core directly, use the numeric address and port that Core
receives the request on so the HTTP `Host` matches its listener. If an HTTPS
reverse proxy forwards to Core, configure the upstream `Host` to match that
listener. Core requests use no redirects or proxy environment settings.

## Supported DWS contract

The Trigger invokes `dws event consume` with `--profile`, `--flatten`, and
`--format ndjson`. A `user_im_message_receive_group` subscription also passes
`--group` with the configured conversation ID. A self-chat-only subscription
passes `--open-dingtalk-id` with the account owner's ID to
`user_im_message_receive_o2o`. For all direct messages it uses
`user_im_message_receive_o2o_all`.

Each consumer needs an `[event] ready` line with its expected `event_key`.
`consumerReady` becomes true when all configured consumers are ready. A
15-second readiness deadline stops the consumers. Stream exit, `bus_shutdown`,
CLI errors, and invalid stream framing stop collection.

| DWS field | Core field |
| --- | --- |
| message_id | sourceMessageId and channel.messageId, preserved in full |
| conversation_id | threadRef.id and channel.conversationId |
| sender_open_dingtalk_id | actor, channel.actorId, native sender address |
| content | text, matched before any group prompt is added |
| event_time (milliseconds) | absolute UTC at, only when present and positive |
| quoted_message | quotedMessage, without changing Thread identity |
| forward_messages | meta.forwardMessages, retained as reference material |
| event_id | sourceEventId, separate from message identity |
| direct sender ID | channel.address.peerOpenDingtalkId, including self-chat |

`sender` is a display name, not actor identity. `human` and `url` are omitted
because this event contract does not prove them. `create_time` lacks a timezone
in the observed event, so it is not used to invent an absolute time. Timestamp
and subscription envelope IDs do not replace the native message ID. Core
generates receivedAt and the reversible Thread key, ordinarily `dws:<CID>`.
The adapter never hashes or truncates native identity. Quoted conversations are
reference material and do not become a discussion root.

Unsupported event types, absent native identity, disallowed conversations,
empty/non-string content and malformed NDJSON produce safe reason codes without
logging external bodies. A line is bounded at 1 MiB. The published flattened
contract has no reliable media-kind discriminator: a textual description already
provided by DWS remains text/reference material. Media is not downloaded or
interpreted; no claim is made that those descriptions contain media contents.
Matching self-chat events are submitted with a `selfSent` hold hint. Core checks
actual outbound message IDs within the same source and conversation and decides
whether to suppress, hold or route the Message.

## Replies and recovery

The adapter uses one loop per process to pull the account's eligible Outbox
items, send or query DWS, and write results back. The loop verifies the joined
source, account and direct-chat address before sending. A Core credential
without Outbox access can collect messages. Its reply loop stops when Core
denies the initial pull.

- DWS replies use `dws chat +messages-reply` with the original conversation,
  message ID and sender. A direct reply also supplies the original peer.
  Set `replyEnabled: true` on a group to allow replies in that group.
  Authorize the CLI's non-interactive confirmation before running an
  instance with Outbox access.
- A send uses the configured profile and an idempotency key derived from the
  Outbox ID and its recorded retry deadline. A retry after an unrecorded
  response reuses the key. A retry scheduled by Core uses a new key.

After an asynchronous send, the adapter saves the channel task ID in the
pending receipt before querying it. A later poll or replacement process queries
that same ID using `dws chat +messages-query-send-status --open-task-id TASK`.
Pending or temporarily unavailable queries leave the task pending. An explicit
missing or expired task allows Core to schedule a new send. A definite success
can include the actual native conversation/message pair, which Core uses for
quoted answers and echo detection.

If Core's response to a result write is lost, the adapter retries the same
write. Known success takes this path directly, even when DWS also reports a
task ID. Core lists retryable and unqueryable failures again after its
60-second retry delay. Permanent failures stay visible in Observer. Loss of a
send before its task or success can be written may lead to a repeated channel
call. Observer shows the resulting Outbox status and the available receipt.

Set `feedback` to add `thinking` to an admitted message in a reply-enabled
conversation. Configure two reusable text emotion templates for the same DWS
profile and keep `feedback.statePath` on persistent storage. After the first
reply is confirmed in the original conversation, feedback replaces `thinking`
with `done` on that message. Feedback uses its own worker and checks the
original message's reactions before retrying an unconfirmed write.

## Acceptance, failure and shutdown

POST `/v1/channel/messages` carries a stable `dws-message:<native message ID>`
operationId, source/account and normalized Message. Only a complete Core
accepted/duplicate receipt counts as durable admission. Lost responses,
unavailable results and unknown outcomes retry the original operation and
identity with exponential backoff (1 second default, capped at 30 seconds).
Conflict and other permanent failures remain visible, preserve Core history and
stop collection. No external body or upstream error text is logged.

Set `databasePath` to an absolute path on local persistent storage, such as
`/var/lib/superagent-triggers/dws.sqlite`. Each instance uses its own private
SQLite database with WAL and a process lock. Use a separate path for the
optional `feedback.statePath`.

The Trigger durably queues each matched Message before tracking feedback or
submitting it. The default `bufferCapacity` is 100 pending items, including the
in-flight head. At capacity the adapter stops its consumers, reports
`stopReason=buffer_full`, and drains the queue before exiting with an error.
Restart drains existing pending items before starting new subscriptions.
The Trigger only deletes an item after Core confirms it. Permanent rejections
remain in SQLite and appear in `permanentFailures`. A database read or write
error stops collection and reports its own error code.

Use `superagent triggers dws status --endpoint <statusEndpoint>` for readiness, admission
state, backlog, counters and the last error. Structured logs also record
connection changes, failures and the final disconnected state.

SIGINT/SIGTERM closes only each owned consumer's stdin pipe and sends SIGTERM
to its PID. No `event stop`, bus kill, process-group signal or shared state
cleanup is used. Shutdown allows five seconds to admit pending messages and
five seconds to confirm consumer exit. An unconfirmed consumer exit returns an
explicit error; there is no forced SIGKILL. Before restarting, inspect that
consumer through DWS. Failed/unknown admission is not reported as success.

The reply loop stops polling and starting new sends during shutdown, and
allows up to five seconds to finish work in progress and report known results.

DWS has no dependable ACK/offset replay contract. Recovery starts at the SQLite
enqueue commit. Messages that arrive during a subscription interruption or
before an enqueue commit depend on upstream redelivery.

## Follow GitHub repositories

Copy `github.config.example.yaml` outside the checkout. Set `repositories` to
the GitHub.com repositories you want to follow. Use `owner/repo` names. Each
repository may include a multiline `prompt` of up to 4096 UTF-8 bytes. The
Trigger includes that prompt in a separate context block after the GitHub
content in `Message.text`. Each repository keeps separate Threads.

Use the optional top-level `match` to select Issues and PRs across every
listed repository. A match on any configured label, assignee login or author
login is sufficient. Matching ignores case. Omit `match` to follow every
Issue and PR in the listed repositories.

```yaml
match:
  labels: [superagent]
  assignees: [alice]
  authors: [my-github-login]
```

Set `githubTokenEnv` to the name of the environment variable that supplies
the GitHub token. Use a separate variable for the Core token. Grant the
GitHub credential read access to the selected repositories, Issues, Pull
requests, Checks, Commit statuses, and Actions.

Use the existing Core `sources` configuration. Match `accountId` and
`instanceId` to the Trigger configuration. Set `coreTokenEnv` to the name of
a separate environment variable that supplies the Core channel token.

```yaml
sources:
  - source: github
    accountId: github-local
    instanceId: github-trigger-1
    tokenFile: /absolute/path/to/github-core.token
```

```sh
bin/superagent triggers github check --config /absolute/path/to/github.yaml
bin/superagent triggers github serve --config /absolute/path/to/github.yaml
bin/superagent triggers github status --endpoint http://127.0.0.1:24372
```

`check` validates the YAML configuration and credentials supplied through the
environment. `serve` uses `githubv4` with Resty's underlying HTTP client to
query GitHub GraphQL. A fresh database starts with a silent baseline for
historical activity.
`pollIntervalSeconds` defaults to 60. Issue and PR lists normally read an
overlapping window of recent updates. The Trigger periodically reconciles
the full lists. It also samples open PRs and reconciles each object's
discussions independently so it can detect edits to older comments.
Only matching objects get discussion, review and CI scans. When an
existing object enters or reenters the match, the Trigger records its
current state as a silent baseline. When it leaves, the Trigger stops
those scans and continues delivering Messages already queued for it.
Retry backoff, the API rate limit, and the number of tracked objects affect
when each scope is read again. `status` shows the last successful read, the
next attempt, errors, the Message backlog, and Core receipts.

Set `databasePath` to an absolute path on local persistent storage, such as
`/var/lib/superagent-triggers/github.sqlite`. The GitHub Trigger uses its own
private SQLite database with WAL and a process lock. Use the same database
and identities when restarting. Each successful scan commits its progress,
changed snapshots, schedules, and pending Messages in one transaction. The
Trigger submits each repository's pending Messages in order and preserves the
original payload through restarts and unknown delivery outcomes. Prompt
changes apply to Messages queued after the change.

Issue observations and comments use the Issue URL as their Thread ID. PR
observations, comments, reviews, and CI share the PR URL in a `pr:url`
Thread. A single existing Thread subscription delivers to its Topic. With
zero or multiple subscriptions, Router reads the Message and chooses the
Topic or ignores the request.

CI summaries identify the PR head SHA and the test merge SHA when available.
They include Check Runs, Commit Statuses, and Workflow Runs with a confirmed
PR relationship. Rate limits, incomplete pages, and permission errors
appear in status. Polling can miss transitions between two samples.

## Replace deployed Triggers

1. Check the old Trigger status and Core Outbox. Record each source, account,
   instance, and persistent state path. Check pending Messages, permanent
   failures, and outstanding DWS send tasks and receipts.
2. Stop the old collectors gracefully so the DWS sender can write known send
   results to Core. Verify the old sender for the account has exited before
   you start its replacement.
3. Back up the stopped Triggers' SQLite state, DWS feedback state if enabled,
   and old configuration. Use state compatible with the new release and keep the
   same source, account, and instance identities. If GitHub still uses v2 JSON
   state, complete the upstream one-time SQLite import before switching.
4. Run each standalone `check`, or start Compose to run its preflight.
   After startup, query each Trigger's local status, verify new receipts in
   Core, and check that pending Messages resume delivery.

To roll back, stop the new collectors and confirm the DWS sender has exited.
Reconcile queued Messages and any DWS send tasks or receipts created during
the cutover. Restart the previous version with its configuration and
compatible state that preserves the reconciled progress.

## Validate

From the repository root, run the offline race suite.

```sh
make test-superagent
```

Tests run real Trigger and Core processes with `internal/testdws` and a local
GitHub GraphQL HTTP fixture. DingTalk tests cover replay/conflict, filtering, native identities,
quote/self input, Core outage/backoff/full-buffer recovery, readiness, malformed
input, disconnect, restart persistence and owned-consumer shutdown. They verify local status and shutdown logs, including recovery after Core becomes
unavailable. Outbound cases use a production
Core to verify task durability, definitive errors, exact echoes and lost write
receipts. The Core build with `-tags coretest` lets crash/restart cases advance
the same Core clock across retry and echo deadlines. GitHub tests cover
repository isolation, pagination, restart recovery, Message replay, PR
registration, routing, and CI transitions. The fixture runs use isolated Core
state and exercise its public API. To run the Trigger suite directly, supply
same-tree binaries in `SUPERAGENT_CORE` and `SUPERAGENT_CORE_CLOCK`. To
include the DWS CLI fixture in race checks, set `TRIGGER_TEST_RACE=1`.
