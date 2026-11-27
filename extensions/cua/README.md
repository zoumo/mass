# Cua MCP

## Configure the relay and computers

Run `make build-cua-mcp` from the MASS repository root. Install `bin/cua-mcp` and Cua CLI 0.31.0 on the massd host. Connect each target computer to the Cua relay with `cua-spacesd` 0.18.0. Confirm the registered ID in the relay's machine list. [Official Cua CLI release](https://github.com/trycua/cua/releases/tag/cua-sdk-v0.31.0) · [Official spacesd release](https://github.com/trycua/cua/releases/tag/cua-spacesd-v0.18.0)

Save the following configuration as `/etc/mass/cua-mcp.yaml` and replace the example paths, relay hostname, and computer ID.

```yaml
cuaCLI: /opt/cua/bin/cua
relayURL: https://relay.example.com:5943
caFile: relay-ca.pem
computers:
  - id: studio-mac
    tokenFile: studio-mac.token
    driver:
      cli: /Users/operator/.local/bin/cua-driver
      socket: /Users/operator/Library/Caches/cua-driver/cua-driver.sock
```

Local relative paths resolve from the configuration file's directory. Set `caFile` to the PEM CA certificate if the relay uses a private CA. When omitted, the clients use their default trust roots. Set `relayURL` to the HTTPS origin. HTTP is accepted for loopback tests. Each `tokenFile` holds one static machine access token and must be a regular file with owner-only permissions such as `0600`.

## Configure the MASS Session

Install the extension at `/opt/mass/bin/cua-mcp`. Put the following list in the Session spec's `mcpServers` field, or use it as the Runtime spec's default `mcpServers` list. The relay credentials stay in the files referenced by the extension configuration.

```json
[
  {
    "name": "cua-mcp",
    "type": "stdio",
    "command": "/opt/mass/bin/cua-mcp",
    "args": ["--config", "/etc/mass/cua-mcp.yaml"]
  }
]
```

Save the list as `cua-mcp-servers.json` to create a Session with an existing Runtime named `cua-runtime`.

```sh
massctl session create desktop -n operations --runtime cua-runtime \
  --cwd /srv/mass/work --mcp-servers-json "$(cat cua-mcp-servers.json)"
```

Use `--mcp-servers-json` on `massctl session load` to apply the same list when importing a native Session. `massctl session resume` uses the saved Session list and the current Runtime defaults. MASS merges the lists by server name. To expose a single MCP server, ensure the Runtime default list is absent, empty, or contains only the same server name. For a Codex Runtime, set `env.CODEX_HOME` to an existing absolute directory dedicated to the Runtime. Keep `mcp_servers` out of that directory's `config.toml`. [MASS Session defaults](../../harness/modules/runtime/session-defaults.md#mcpservers)

## Run a short command

Call `space_bash` with `command` and optional `timeout` in seconds. Pass `computer_id` when the configuration lists multiple computers. Each computer uses the relay path `/m/<computer_id>`. The first call starts a Cua MCP subprocess for that computer. The server reuses it until the stdio connection closes and then removes its temporary Cua state.

## Desktop tools

Add `driver` to a computer with a running standalone Cua Driver on macOS. Grant the Driver app Accessibility and Screen Recording access. Set `cli` and `socket` to absolute paths on that Mac. Use the socket of the authorized Driver app. Computers without `driver` still support `space_bash` and PTY tools.

The desktop tools are `list_windows`, `get_window_state`, `click`, `type_text`, and `press_key`. Their input schemas come from Cua Driver 0.63.1. [Cua Driver release](https://github.com/trycua/cua/releases/tag/cua-driver-rs-v0.63.1)

Pass the same `computer_id` when reading a window and acting on an element token from that read. For each computer, the first desktop call opens a Driver MCP session over the relay. Later desktop calls reuse that session for the lifetime of the stdio connection.

Request a screenshot through `get_window_state` with `include_screenshot: true`. Omit `screenshot_out_file` to receive the PNG as an MCP image content block.

## Run an interactive command

1. Call `pty_start` with an `operation_id`, the command as `argv`, and an optional working directory as `cwd`. Pass `computer_id` if multiple computers are configured. If the start outcome is uncertain, retry with the same ID and arguments.
2. Call `pty_read` with the returned `handle` and `cursor`. Keep reading from `next_cursor` until the program asks for input. Read `data_base64` when you need the original bytes. A `lost` range reports bytes the target can no longer return.
3. Call `pty_write` separately with the same handle and `sequence: 1`. Use `text` or `data_base64`. Include a newline when the program expects a line. If the result is uncertain, retry with the same sequence and bytes. Increase the sequence after a confirmed write.
4. Call `pty_read` again with the last confirmed cursor. Read the output through `output_end_cursor` and check the exit code once the process reports `exited`. To terminate a running process, call `pty_stop` and read back its exit status.

After a relay interruption, retry the read with the original handle and last confirmed cursor once the target reconnects. The handle remains valid while the same `cua-mcp` connection and `cua-spacesd` process are alive. A Session operation that opens a fresh stdio connection requires new PTY handles.
