---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T03: Wire Store initialization into agentd daemon

Wire Store into agentd daemon startup and shutdown. Update cmd/agentd/main.go to: create parent directory for MetaDB path if not exists, initialize Store from cfg.MetaDB after config parsing, pass Store to future managers (placeholder for now), add Store.Close() to shutdown sequence after ARI server shutdown. Create integration test that starts agentd with minimal config including MetaDB path, verifies Store created, sends SIGTERM, verifies shutdown completes. Re-verify R001 daemon launchability with Store initialized.

## Inputs

- `pkg/meta/store.go`
- `pkg/agentd/config.go`
- `cmd/agentd/main.go`

## Expected Output

- `cmd/agentd/main.go`
- `pkg/meta/integration_test.go`

## Verification

go build -o bin/agentd ./cmd/agentd && go test ./pkg/meta/... -v -run TestIntegration

## Observability Impact

Signals added: main.go logs Store initialization and Close. Inspection: daemon logs show database path. Failure state: Store init failure logged and daemon exits
