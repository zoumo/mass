# MASS quick start

Run the commands from the repository root with Go 1.26.9 or later.

## Run the offline demonstration

The demonstration requires the Go dependencies in your local cache.

```sh
make prototype-go
```

The demo starts temporary `massd` and `massctl` processes with synthetic Codex
and Claude protocol peers. A passing run verifies Session creation, input,
interactions, live events, saved history, cancellation, close, and resume. It
requires neither an agent account nor a model call.

## Start a local daemon

1. Build the daemon and CLI.

   ```sh
   make build-go
   ```

2. Start `massd` with a fresh data directory and a free loopback port. Pass an
   empty YAML file to use a separate configuration for this run.

   ```sh
   run_dir=$(mktemp -d)
   printf '{}\n' > "$run_dir/config.yaml"
   bin/massd --config "$run_dir/config.yaml" --data "$run_dir/data" --listen 127.0.0.1:0
   ```

   Wait for `massd` to print a JSON object with a `url` such as
   `ws://127.0.0.1:49152/jsonrpc`.

3. In another terminal, replace `PORT` with the port in the printed URL.

   ```sh
   bin/massctl --url ws://127.0.0.1:PORT/jsonrpc runtime list
   ```

   The list contains `claude` and `codex`. The daemon creates missing entries
   at startup. Each new entry sets `fullAccess` as the default permission
   preset for its Sessions.

4. Press Ctrl+C in the daemon terminal to stop the service.

The Runtime list is available before you install the native CLIs. To create a
Session and send input, install and sign in to the corresponding agent CLI in
the daemon environment. Put that CLI on the daemon's `PATH` or configure its
absolute executable path. Follow the [massd guide](../massd/README.md) to create
and observe a Session.

## Local defaults

| Setting | Default |
|---|---|
| WebSocket endpoint | `ws://127.0.0.1:24368/jsonrpc` |
| Data directory | `~/.mass` |
| Configuration file | `~/.mass/config.yaml`, when present |

Flags override file values. When `tokenFile` is unset, loopback RPC connections
require no authentication. To require a Bearer token, set `tokenFile` in the
YAML or pass `--token-file` to `massd`. Supply the same token to `massctl`
through `MASS_TOKEN`. Direct remote access requires a token and TLS.
