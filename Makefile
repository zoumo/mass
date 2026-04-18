BIN_DIR := bin

# Auto-discover commands: each subdirectory under cmd/ with a main.go is a command
COMMANDS := $(notdir $(patsubst %/main.go,%,$(wildcard cmd/*/main.go)))


.PHONY: build
fast-build: tidy $(COMMANDS)

.PHONY: build
build: tidy test $(COMMANDS)

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: $(COMMANDS)
$(COMMANDS):
	go build -o $(BIN_DIR)/$@ ./cmd/$@

.PHONY: test
test: fmt lint
	go test ./...

GOLANGCI_LINT_VERSION ?= v2.11.4
GOLANGCI_LINT := $(shell go env GOBIN)/golangci-lint

.PHONY: fmt
fmt: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) fmt ./...

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

$(GOLANGCI_LINT):
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: coverage
coverage:
	go test -coverprofile=coverage.out ./...
	@echo "Coverage report written to coverage.out"
	@go tool cover -func=coverage.out | tail -1

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out
