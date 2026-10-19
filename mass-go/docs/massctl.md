# massctl

Go CLI for the Go and Rust MASS servers. Its Cobra command tree, persistent
options, table/JSON output and JSONL events follow agentrun's `agentdctl`.
Development follows the [MASS HTTP binding draft](../../harness/bindings/http.md).
The commands below describe current server behavior, not conformance to every new contract.
The CLI lives in `mass-go/cmd/massctl`, alongside the Go server, and shares
the `mass-go` module. It interacts with runtimes through HTTP/SSE.

The same CLI also connects to the [Go HTTP/SSE server](http-server.md)
with `--endpoint http://127.0.0.1:24369`. Build both with `make build-go`.
For Go Claude sessions, supply `session new NAME --runtime-name claude
--cwd /absolute/workspace --prompt 'Say hello'`. The `--prompt` creation flag is
Go-only. Go receipts, events, in-memory history and resume limits follow the Go
server contract; the Rust behavior described below is unchanged.

From the repository root, with Go 1.26.9:

```sh
make build-massctl
./dist/massctl --help
./dist/massctl status
./dist/massctl runtime types
./dist/massctl runtime list
./dist/massctl runtime status local-codex
./dist/massctl session new demo --runtime-name local-codex --cwd /absolute/workspace
./dist/massctl session list
./dist/massctl session status demo --json
./dist/massctl session prompt demo 'Explain this project'
./dist/massctl session events demo --after-seq 0 --limit 100
./dist/massctl session events demo --after-seq 100 --limit 100 --page --json
./dist/massctl session watch demo
./dist/massctl session cancel demo
./dist/massctl session archive demo
./dist/massctl session resume demo
```

Use `--endpoint http://host:24368` or the full
`http://host:24368/api/v1/agentd/jsonrpc` URL. Default:
`http://127.0.0.1:24368`. Global flags can appear after subcommands. Unary calls
default to `--timeout 60s`. Watch uses this timeout for response headers only and
continues until EOF or Ctrl-C. Ctrl-C closes observation; it does not cancel the
Agent's work. Run `session cancel` explicitly to request interruption.

Lists default to tables. `--json` or `-o json` emits compact JSON;
other single results default to indented JSON. Historical and live events are
JSONL with their original fields and integer precision preserved. `events`
reads exactly one page (default 100, maximum 1000). `--after-seq` is exclusive.
`--page` emits the full `{events,nextSeq}` response; use nextSeq for the next page.
Watch is live-only, without replay or reconnect. It may include transient events
without a seq. Open watch in another terminal before submitting a prompt to see
live progress. A stream EOF does not prove work completion.

First-version scope follows existing server capabilities:

| Resource | Commands |
| --- | --- |
| Server | `status` |
| Runtime (`rt`) | `types`, `list`, `status` (`get`) |
| Session (`s`) | `new` (`create`), `list`, `status` (`get`), `prompt`, `cancel`, `archive` (`delete`), `resume` |
| Session event | `get-event` (`events`, `get-events`, `get_event`), `watch` (`watch-event`, `watch-events`, `watch_event`) |

Runtime creation/update/deletion remains startup-file managed. There is no
session spec update or physical delete API. `session delete` archives the
attachment and preserves identity and history; it never removes files. Runtime
configuration, default-runtime selection, namespace, Unix sockets, replay watches
and `prompt --wait` from agentrun are not part of the MASS contract.
`set_config_option` is always unsupported by the server and has no CLI command.
For remote runtimes, `--cwd` is an absolute path on the remote host; the CLI does
not check local existence. Resume availability is determined by the server.

RPC failures exit nonzero and retain error code and structured data, including
operationId and unknown acceptance evidence, on stderr. Requests are never
automatically retried or redirected. Timeout/disconnect does not prove an
accepted server operation was canceled; inspect status/history before deciding
what to do next. Prompt returns acceptance evidence, not completed output.

```sh
make test-massctl
make lint-massctl
make check-massctl
```

Tests use HTTP fixtures and never call a model or use local Agent credentials.
