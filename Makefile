RUST_MANIFEST := mass-rs/Cargo.toml

# Explicit local toolchain override, e.g. CARGO_FLAGS=--ignore-rust-version.
# Empty by default: supported builds retain upstream MSRV enforcement.
CARGO_FLAGS ?=
# Upstream embedded-client tests overflow Rust's default 2 MiB test stack.
# Scope these defaults to test processes; production launches need no setting.
RUST_MIN_STACK ?= 16777216
RUST_TEST_THREADS ?= 2

.PHONY: fmt lint test test-native build check release verify-release fmt-massctl lint-massctl test-massctl build-massctl check-massctl

# Native macOS arm64 builds only. Linux delivery is deferred.
RELEASE_DIST ?= dist

release:
	python3 mass-rs/scripts/build_release.py --dist $(RELEASE_DIST)

verify-release:
	python3 mass-rs/scripts/build_release.py --dist $(RELEASE_DIST) --verify-only

fmt: fmt-massctl
	cargo fmt --manifest-path $(RUST_MANIFEST) --all

lint: lint-massctl
	cargo clippy --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --all-features --locked -- -D warnings

test: test-massctl
	RUST_MIN_STACK=$(RUST_MIN_STACK) RUST_TEST_THREADS=$(RUST_TEST_THREADS) cargo test --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --locked

# Explicit real Codex/model acceptance. Missing dependencies fail, never skip.
# NATIVE_TEST optionally selects one Rust test by name.
test-native:
	@test -n "$(MASS_TEST_CODEX)" || (echo 'MASS_TEST_CODEX must name the reviewed absolute Codex binary'; exit 1)
	@test -n "$(MASS_TEST_CODEX_SHA256)" || (echo 'MASS_TEST_CODEX_SHA256 must contain its reviewed SHA256'; exit 1)
	@set -eu; artifacts=$$(mktemp /tmp/mass-native-artifacts.XXXXXX); \
	cargo test --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --locked -p mass-daemon -p mass-codex --tests --no-run --message-format=json > "$$artifacts"; \
	MASS_TEST_ARTIFACTS="$$artifacts" MASS_TEST_FILTER="$(NATIVE_TEST)" RUST_MIN_STACK=$(RUST_MIN_STACK) cargo test --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) -p mass-daemon --test native_stage --locked -- --ignored --test-threads=1 --nocapture

build: build-massctl
	cargo build --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --locked

check: check-massctl
	python3 mass-rs/scripts/check_workspace.py
	python3 mass-rs/scripts/check_docs.py
	cargo fmt --manifest-path $(RUST_MANIFEST) --all -- --check
	cargo clippy --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --all-features --locked -- -D warnings
	RUST_MIN_STACK=$(RUST_MIN_STACK) RUST_TEST_THREADS=$(RUST_TEST_THREADS) cargo test --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --locked
	cargo build --manifest-path $(RUST_MANIFEST) $(CARGO_FLAGS) --workspace --all-targets --locked

fmt-massctl:
	cd mass-go && gofmt -w cmd/massctl internal/commands internal/client

lint-massctl:
	cd mass-go && go vet ./cmd/massctl ./internal/commands ./internal/client

test-massctl:
	cd mass-go && go test -race ./cmd/massctl ./internal/commands ./internal/client

build-massctl:
	mkdir -p dist
	cd mass-go && go build -trimpath -o ../dist/massctl ./cmd/massctl

check-massctl: lint-massctl test-massctl build-massctl
	cd mass-go && test -z "$$(gofmt -l cmd/massctl internal/commands internal/client)"

.PHONY: build-go test-go
build-go: build-massctl
	cd mass-go && go build -trimpath -o ../dist/massd-go ./cmd/massd

test-go: test-massctl
	cd mass-go && go test ./...
