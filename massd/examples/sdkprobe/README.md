# Run the SDK protocol probe

From the repository root, use the Go toolchain in [go.mod](../../../go.mod), cached dependencies, and `ps` on `PATH`.

```sh
GOWORK=off GOPROXY=off GOSUMDB=off go test -mod=readonly -count=1 -v ./massd/examples/sdkprobe
```

The [probe](probe_test.go) drives the pinned SDK directly through authored JSONL peers. Use the test and [scenarios](testdata/) to inspect expected frames, original interaction handles, and cleanup checks. The same probe runs in `make test-integration-go`.

A passing probe establishes protocol behavior for the authored frames. Tool results and the no-input Claude identity in positive cases come from synthetic frames. Check the [historical evidence](evidence/README.md) for the exact commands and scope of earlier runs.
