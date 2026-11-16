# Runtime / Sessions observation

Open `/observer/resources`. The default `scope=associated` joins resources only
through durable Session IDs in Router, Assignment, Worker and pinned delivery /
inbox references. Namespace is a display/filter value. `scope=all` reads all
resources of the configured massd instance, including offline and unassociated
Sessions, and uses whole-instance reverse associations even when a Project is
selected in the page shell. Historical Router Runtime ownership is resolved by
its pinned Session ID, so changing the current Router Runtime cannot hide it. A selected Session that was deleted remains unavailable; observation
never creates, loads, resumes, prompts, cancels or closes execution.

The browser and authenticated `mass-sa observe` commands use `Observer.Query`.
Examples (use the same operator configuration as `serve`):

```sh
mass-sa observe runtimes --config /path/to/config.yaml --scope all --type claude
mass-sa observe runtime runtime-a --config /path/to/config.yaml
mass-sa observe sessions --config /path/to/config.yaml --scope all --namespace shared --state offline --limit 50
mass-sa observe session SESSION_ID --config /path/to/config.yaml --scope all
mass-sa observe history SESSION_ID --config /path/to/config.yaml --event-types agent.message,agent.thinking
mass-sa observe history SESSION_ID --config /path/to/config.yaml --window page --cursor NATIVE_HISTORY_CURSOR
```

Session-list cursors wrap the unchanged native cursor and bind the local scope,
Project and native filters. History cursors remain native and are never inferred
from event IDs or timestamps. `runtime.list` is unpaginated in the public protocol.
Observer freezes its filtered Runtime result in a local snapshot and pages it by
stable Runtime identity, default 50 and maximum 200 records. Its cursor is local,
binds scope/Project/filters, expires after 15 minutes or restart, and is never
sent to MASS. At most 64 Runtime traversals remain cached. Refresh starts a new
snapshot. Resource queries reject unknown/repeated parameters and unknown Projects.
URL `resourceTab=sessions|runtimes` selects the resource type; `selected` is the
exact Session ID or Runtime name.
`historyCursor` with `historyWindow=page` restores the earliest explicitly loaded
native history page on reload; `follow=paused` restores paused display. These
browser navigation parameters are translated to the history API's native
`cursor`/`window`; they are not forwarded as arbitrary upstream filters.
Expanded event identities, focused controls and scroll positions are stored
only in local session storage, without event text or raw payload.

History opens the recent native page by scanning forward with a bounded budget
(1024 pages, per-request timeout), retaining previous page start cursors for
“load earlier”. `window=page` starts at the beginning or a supplied native cursor.
An exceeded recent-scan budget returns an explicit error. Filtering supports the
five persistent SessionEvent types. Errors/state changes are live observations
because massd does not persist them in `session.history`.

The selected Session's SSE route opens a dedicated initialized WebSocket and
waits for subscribe before reading history. A bounded upstream queue covers the
window. Live data remains a preview until confirmed by durable history. The
browser aggregates preview chunks by original item identity, retains source
IDs and replaces previews with full facts; tool call and result remain distinct.
History stays in append order, even if IDs or event times are not ordered.

The public history API does not return a cursor after its terminal page, so the
observer retains that page's *start* cursor and rereads its bounded overlap.
SSE emits `history`, `preview`, `checkpoint` and `status`. The browser reconnects
with the latest checkpoint (`window=page` also when the native cursor is empty), merges the overlap by original identity and always
warns that nonpersistent previews may be missing. Connection loss retains old
data and the disconnect time; a source error is never converted to an empty list.

Pause follow freezes displayed events and expanded reading position while the
bounded observation continues. Manual earlier-history loading remains available.
Leaving the page or changing selection closes only observation connections.
The browser keeps at most 500 durable facts and 200 raw preview events; upstream
subscription queues, including setup, hold at most 256 events. Overflow returns
`slow_subscriber` and closes the observation. SSE clears the inherited write
deadline only on that response, sets a 5-second deadline per write, and sends
periodic history checkpoints. Other HTTP routes retain the 30-second server
WriteTimeout. Service shutdown cancels active observation request contexts.

Runtime / Session configuration uses display allowlists. No env, authentication
headers, command arguments, capability paths or arbitrary MCP configuration is
forwarded. Requested model/effort and persistent initial configuration have
separate labels; provider model stays “未观测” without explicit public evidence.
`_meta` is omitted. Tool raw JSON is sanitized and rendered as text, with known
sensitive keys masked; arbitrary free text uses the existing common-secret
masking, which is not a claim to detect every secret. Display text / raw JSON
have explicit truncation markers. HTML and remote content blocks are inert.

Checks: `GOWORK=off go test -race ./...`; build web assets before production-tag
checks, then `GOWORK=off go test -tags observerweb -race -run '^TestObserver' ./...`.
Resource browser fixture uses isolated loopback port 18795:
`cd web && npm exec playwright test -- --config=playwright.resources.config.ts`.
The original Router fixture on 18788 remains separate. These are synthetic
protocol/process/browser checks, not real massd/provider acceptance.
