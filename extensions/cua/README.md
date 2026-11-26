# Cua MCP

Build the stdio server with `make build-cua-mcp`. Launch `bin/cua-mcp --config /path/to/cua-mcp.yaml` as the MCP command. Use Cua CLI 0.31.0. [Official Cua CLI release](https://github.com/trycua/cua/releases/tag/cua-sdk-v0.31.0)

```yaml
cuaCLI: /opt/cua/bin/cua
relayURL: https://relay.example.com:5943
caFile: relay-ca.pem
computers:
  - id: studio-mac
    tokenFile: studio-mac.token
```

Relative paths resolve from the configuration file's directory. Omit `caFile` to use the Cua CLI's default trust store. `relayURL` is an HTTPS origin. HTTP is accepted for loopback tests. Each `tokenFile` holds one static access token and must be a regular file with owner-only permissions such as `0600`.

Call `space_bash` with `command` and optional `timeout` in seconds. Pass `computer_id` when the configuration lists multiple computers. Each computer uses the relay path `/m/<computer_id>`. The first call starts a Cua MCP subprocess for that computer. The server reuses it until the stdio connection closes and then removes its temporary Cua state.
