---
estimated_steps: 1
estimated_files: 4
skills_used: []
---

# T03: Create pkg/ari/client/client.go and update cmd entrypoints

Create Dial helper. Update cmd/agentd/subcommands/server/command.go to use pkg/ari/server + jsonrpc.Server. Update cmd/agentd/subcommands/shim/command.go to use pkg/shim/server. Update pkg/agentd/process.go to use pkg/shim/client. Verify make build + go test ./...

## Inputs

- `pkg/ari/server/server.go`
- `pkg/shim/server/service.go`
- `pkg/agentd/shim_client.go`
- `cmd/agentd/subcommands/server/command.go`
- `cmd/agentd/subcommands/shim/command.go`

## Expected Output

- `pkg/ari/client/client.go`

## Verification

make build && go test ./...
