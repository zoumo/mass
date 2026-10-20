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
	cd mass-go && go test -race -timeout=180s ./...

build-massctl:
	mkdir -p bin
	cd mass-go && go build -trimpath -o ../bin/massctl ./cmd/massctl

build-go: build-massctl
	cd mass-go && go build -trimpath -o ../bin/massd-go ./cmd/massd

check-go: lint-go test-go build-go
	cd mass-go && golangci-lint fmt --diff

fmt-massctl: fmt-go
lint-massctl: lint-go
test-massctl: test-go
check-massctl: check-go

# Isolated synthetic end-to-end acceptance. Builds both pinned SDK mocks plus
# massd/massctl, exercises the product entrypoints, and reads cleanup back.
.PHONY: prototype-go
prototype-go:
	cd mass-go && GOWORK=off GOPROXY=off go test -count=1 -v -timeout=180s ./tests/vertical -run '^TestEntrypointSuite$$'
