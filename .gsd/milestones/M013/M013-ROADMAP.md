# M013: Package Restructure - Clean api/ Boundary + Event/Runtime Colocation

## Vision
Complete the package restructure defined in docs/plan/package-restructure-20260414.md: migrate all consumers off old import paths (api/runtime, api/ari, api/shim, api/), distribute service interfaces and client wrappers to server/ and client/ subdirectories, relocate pkg/events/ into pkg/shim/, and move pkg/runtime/ to pkg/shim/runtime/acp/. The result is a codebase where api/ subdirectories contain only pure types (struct/const/enum) and all implementation code lives in typed server/ or client/ packages.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | S01 | medium | — | ✅ | After this: make build + go test ./... pass with no imports of api/runtime or api (for Status/EnvVar); api/runtime/ and api/types.go deleted; empty runtimeclass stub files deleted. |
| S02 | S02 | medium | — | ✅ | After this: make build + go test ./... pass; pkg/ari/api/ has types.go+domain.go+methods.go; pkg/ari root has only api/, server/, client/ subdirs; no imports of api/ari remain. |
| S03 | S03 | high | — | ✅ | After this: make build + go test ./... pass; pkg/shim/api/ has all shim type files; no imports of api/shim, api (methods/events), remain; api/ directory is gone. |
| S04 | S04 | medium | — | ✅ | After this: make build + go test ./... + go vet ./... all pass; pkg/events/ and pkg/runtime/ do not exist; pkg/shim/server/ has translator.go and log.go; pkg/shim/runtime/acp/ has the ACP runtime. |
