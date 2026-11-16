.PHONY: observer-build observer-check

observer-build:
	cd web && npm ci --no-fund --no-audit && npm run build
	GOWORK=off go build -tags observerweb -o bin/mass-sa ./cmd/mass-sa

observer-check:
	cd web && npm ci --no-fund --no-audit && npm run build
	GOWORK=off go test -tags observerweb -race ./...
	GOWORK=off go vet -tags observerweb ./...
