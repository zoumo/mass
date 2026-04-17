BIN_DIR := bin

.PHONY: build clean mass massctl test lint coverage

build: mass massctl

mass:
	go build -o $(BIN_DIR)/mass ./cmd/mass

massctl:
	go build -o $(BIN_DIR)/massctl ./cmd/massctl

test:
	go test ./...

GOLANGCI_LINT ?= $(shell find "$${HOME}/.local/share/mise/installs/golangci-lint" -name golangci-lint -type f 2>/dev/null | head -1)
ifeq ($(GOLANGCI_LINT),)
GOLANGCI_LINT := golangci-lint
endif

lint:
	$(GOLANGCI_LINT) run ./...

coverage:
	go test -coverprofile=coverage.out ./...
	@echo "Coverage report written to coverage.out"
	@go tool cover -func=coverage.out | tail -1

clean:
	rm -rf $(BIN_DIR) coverage.out
