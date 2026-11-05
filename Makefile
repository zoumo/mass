.PHONY: fmt lint test build check fmt-massctl lint-massctl test-massctl build-massctl check-massctl

fmt: fmt-go
lint: lint-go
test: test-go
build: build-go
check: check-go

# Go experimental Service-backed CLI and HTTP/SSE.
.PHONY: fmt-go lint-go test-go build-go check-go
fmt-go:
	cd mass-go && golangci-lint fmt

lint-go:
	cd mass-go && golangci-lint run ./...

test-go:
	# The full vertical race suite takes about six minutes; retain a bounded budget.
	@test -n "$(AGENT_SDK_TESTIFY_BIN)" || { echo "AGENT_SDK_TESTIFY_BIN must name an absolute prebuilt agent-sdk-testify executable" >&2; exit 2; }
	cd mass-go && go test -race -timeout=600s ./...

build-massctl:
	mkdir -p bin
	cd mass-go && go build -trimpath -o ../bin/massctl ./cmd/massctl

build-go: build-massctl
	cd mass-go && go build -trimpath -o ../bin/massd ./cmd/massd

check-go: lint-go test-go build-go
	cd mass-go && golangci-lint fmt --diff

.PHONY: recording-gate-go
recording-gate-go:
	cd mass-go && MASS_RECORD=0 GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 -v -timeout=300s ./tests/recording

fmt-massctl: fmt-go
lint-massctl: lint-go
test-massctl: test-go
check-massctl: check-go

# Isolated synthetic end-to-end acceptance. Builds both MASS scenario peers plus
# massd/massctl, exercises the product entrypoints, and reads cleanup back.
.PHONY: prototype-go
prototype-go:
	@test -n "$(AGENT_SDK_TESTIFY_BIN)" || { echo "AGENT_SDK_TESTIFY_BIN must name an absolute prebuilt agent-sdk-testify executable" >&2; exit 2; }
	cd mass-go && GOWORK=off GOPROXY=off go test -count=1 -v -timeout=180s ./tests/vertical -run '^TestEntrypointSuite$$'
