.PHONY: fmt lint test build check fmt-massctl lint-massctl test-massctl build-massctl check-massctl

# Use go.mod even if a checkout retains a local vendor directory.
export GOFLAGS := $(strip $(GOFLAGS) -mod=readonly)

# Cached lint diagnostics contain absolute source paths.
export GOLANGCI_LINT_CACHE ?= $(CURDIR)/.cache/golangci-lint

mass_commands := ./cmd/massd/... ./cmd/massctl/... ./cmd/mass-record-diagnose/... ./cmd/cua-mcp/...
cua_packages := ./extensions/cua/...
mass_packages := ./massd/... ./internal/... $(mass_commands) $(cua_packages)
superagent_packages := ./superagent/... ./cmd/superagent/...

fmt: fmt-go fmt-superagent
lint: lint-go lint-superagent
test: test-go test-superagent check-superagent-web
build: build-go build-superagent
check: check-go check-superagent

# MASS session service and CLI.
.PHONY: fmt-go lint-go test-go test-unit-go test-integration-go test-simulation-go test-e2e-go build-go check-go
fmt-go:
	golangci-lint fmt $(mass_packages)

lint-go:
	golangci-lint run $(mass_packages)

# Package-local tests: no integration suite or recorded material.
test-unit-go:
	GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -timeout=600s ./massd ./massd/pkg/... ./internal/... $(mass_commands) $(cua_packages)

# Constructed multi-module scenarios and strict SDK protocol probes.
test-integration-go:
	GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -timeout=900s ./massd/tests/integration ./massd/examples/sdkprobe

# Recorded consumer material through agent-replay; never launches an Agent.
test-simulation-go:
	MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -v -timeout=600s ./massd/tests/simulation

# Explicit real-CLI/model recording and E2E. Never part of test-go or check-go.
test-e2e-go:
	MASS_RECORD=1 GOWORK=off GOPROXY=off GOSUMDB=off go test -tags=e2e -count=1 -v -timeout=600s ./massd/tests/e2e

test-go:
	$(MAKE) test-unit-go
	$(MAKE) test-integration-go
	$(MAKE) test-simulation-go

build-massctl:
	mkdir -p bin
	go build -trimpath -o bin/massctl ./cmd/massctl

.PHONY: build-cua-mcp
build-cua-mcp:
	mkdir -p bin
	go build -trimpath -o bin/cua-mcp ./cmd/cua-mcp

build-go: build-massctl build-cua-mcp
	go build -trimpath -o bin/massd ./cmd/massd

check-go: lint-go test-go build-go
	golangci-lint fmt --diff $(mass_packages)

.PHONY: recording-gate-go
recording-gate-go: test-simulation-go

fmt-massctl: fmt-go
lint-massctl: lint-go
test-massctl: test-go
check-massctl: check-go

# Isolated synthetic end-to-end acceptance. Builds both MASS scenario peers plus
# massd/massctl, exercises the product entrypoints, and reads cleanup back.
.PHONY: prototype-go
prototype-go:
	GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=600s ./massd/tests/integration -run '^TestEntrypointSuite$$'

# Offline cross-capability combinations; no recording or real-model fallback.
.PHONY: acceptance-sdk-go
acceptance-sdk-go:
	GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -v -timeout=600s ./massd/tests/integration -run '^TestTicket08'

# Superagent Core and Superagent Web are part of the root offline gates.
.PHONY: fmt-superagent lint-superagent test-superagent vet-superagent build-superagent check-superagent build-superagent-web check-superagent-web openapi-superagent check-superagent-openapi
fmt-superagent:
	golangci-lint fmt $(superagent_packages)
lint-superagent:
	golangci-lint run $(superagent_packages)
	golangci-lint run --build-tags coretest $(superagent_packages)
test-superagent:
	set -eu; \
		export GOWORK=off GOPROXY=off GOSUMDB=off; \
		test_bin=$$(mktemp -d "$${TMPDIR:-/tmp}/superagent-test.XXXXXX"); \
		trap 'rm -rf "$$test_bin"' EXIT; \
		trap 'exit 1' HUP INT TERM; \
		go build -race -trimpath -o "$$test_bin/superagent" ./cmd/superagent; \
		go build -race -trimpath -tags coretest -o "$$test_bin/superagent-coretest" ./cmd/superagent; \
		SUPERAGENT_CORE="$$test_bin/superagent" SUPERAGENT_CORE_CLOCK="$$test_bin/superagent-coretest" \
			SUPERAGENT_TRIGGER_BINARY= TRIGGER_TEST_RACE=1 go test -race -count=1 -v -timeout=600s $(superagent_packages)
vet-superagent:
	GOWORK=off GOPROXY=off GOSUMDB=off go vet $(superagent_packages)
	GOWORK=off GOPROXY=off GOSUMDB=off go vet -tags coretest $(superagent_packages)
build-superagent-web:
	cd superagent/web && npm ci --offline --no-audit --no-fund
	cd superagent/web && npm run build
build-superagent: build-superagent-web
	mkdir -p bin
	GOWORK=off GOPROXY=off GOSUMDB=off go build -trimpath -tags superagentweb -o bin/superagent ./cmd/superagent
check-superagent-web: build-superagent-web
	cd superagent/web && npm exec -- playwright test
openapi-superagent:
	@GOWORK=off GOPROXY=off GOSUMDB=off go run ./cmd/superagent openapi
check-superagent-openapi:
	@set -eu; \
		spec=$$(mktemp); \
		trap 'rm -f "$$spec"' EXIT; \
		$(MAKE) --no-print-directory openapi-superagent > "$$spec"; \
		diff -u superagent/openapi.json "$$spec"
check-superagent: lint-superagent test-superagent vet-superagent build-superagent check-superagent-web check-superagent-openapi
	golangci-lint fmt --diff $(superagent_packages)
