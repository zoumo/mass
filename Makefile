.PHONY: fmt lint test build check fmt-massctl lint-massctl test-massctl build-massctl check-massctl

fmt: fmt-go fmt-superagent
lint: lint-go lint-superagent
test: test-go test-superagent observer-check
build: build-go build-superagent
check: check-go check-superagent

# Go experimental Service-backed CLI and HTTP/SSE.
.PHONY: fmt-go lint-go test-go test-unit-go test-integration-go test-simulation-go test-e2e-go build-go check-go
fmt-go:
	cd mass-go && golangci-lint fmt

lint-go:
	cd mass-go && golangci-lint run ./...

# Package-local tests: no integration suite or recorded material.
test-unit-go:
	cd mass-go && GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -timeout=600s ./pkg/... ./cmd/... ./internal/...

# Constructed multi-module scenarios and strict SDK protocol probes.
test-integration-go:
	cd mass-go && GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -timeout=900s ./tests/integration ./examples/sdkprobe

# Recorded consumer material through agent-sdk-testify; never launches an Agent.
test-simulation-go:
	cd mass-go && MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -v -timeout=600s ./tests/simulation

# Explicit real-CLI/model recording and E2E. Never part of test-go or check-go.
test-e2e-go:
	cd mass-go && MASS_RECORD=1 GOWORK=off GOPROXY=off GOSUMDB=off go test -tags=e2e -count=1 -v -timeout=600s ./tests/e2e

test-go:
	$(MAKE) test-unit-go
	$(MAKE) test-integration-go
	$(MAKE) test-simulation-go

build-massctl:
	mkdir -p bin
	cd mass-go && go build -trimpath -o ../bin/massctl ./cmd/massctl

build-go: build-massctl
	cd mass-go && go build -trimpath -o ../bin/massd ./cmd/massd

check-go: lint-go test-go build-go
	cd mass-go && golangci-lint fmt --diff

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
	cd mass-go && GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=600s ./tests/integration -run '^TestEntrypointSuite$$'

# Offline cross-capability combinations; no recording or real-model fallback.
.PHONY: acceptance-sdk-go
acceptance-sdk-go:
	cd mass-go && GOWORK=off GOPROXY=off GOSUMDB=off go test -race -count=1 -v -timeout=600s ./tests/integration -run '^TestTicket08'

# The Superagent Go module and read-only Observer are part of the root offline gates.
.PHONY: fmt-superagent lint-superagent test-superagent vet-superagent build-superagent check-superagent observer-build observer-check
fmt-superagent:
	cd superagent && golangci-lint fmt
lint-superagent: observer-build
	cd superagent && golangci-lint run ./...
	cd superagent && golangci-lint run --build-tags observerweb,coretest ./...
test-superagent:
	set -eu; cd superagent; \
		export GOWORK=off GOPROXY=off GOSUMDB=off; \
		test_bin=$$(mktemp -d "$${TMPDIR:-/tmp}/superagent-test.XXXXXX"); \
		trap 'rm -rf "$$test_bin"' EXIT; \
		trap 'exit 1' HUP INT TERM; \
		go build -race -trimpath -o "$$test_bin/superagent" ./cmd/superagent; \
		go build -race -trimpath -tags coretest -o "$$test_bin/superagent-coretest" ./cmd/superagent; \
		SUPERAGENT_CORE="$$test_bin/superagent" SUPERAGENT_CORE_CLOCK="$$test_bin/superagent-coretest" \
			SUPERAGENT_TRIGGER_BINARY= TRIGGER_TEST_RACE=1 go test -race -count=1 -v -timeout=600s ./...
vet-superagent:
	cd superagent && GOWORK=off GOPROXY=off GOSUMDB=off go vet ./...
	cd superagent && GOWORK=off GOPROXY=off GOSUMDB=off go vet -tags observerweb,coretest ./...
observer-build:
	cd superagent/web && npm ci --offline --no-audit --no-fund
	cd superagent/web && npm run build
build-superagent: observer-build
	mkdir -p bin
	cd superagent && GOWORK=off GOPROXY=off GOSUMDB=off go build -trimpath -tags observerweb -o ../bin/superagent ./cmd/superagent
observer-check: observer-build
	cd superagent/web && npm run test:e2e
check-superagent: lint-superagent test-superagent vet-superagent build-superagent observer-check
	cd superagent && golangci-lint fmt --diff
