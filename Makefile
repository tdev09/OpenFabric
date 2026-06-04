.PHONY: dev dev-agent ui build build-all test lint clean release help

BINARY  := fabric
DIST    := dist
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
PORT    := 4892

## help: Show this help message
help:
	@echo "OpenFabric Makefile"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@grep -E '^## ' Makefile | sed 's/## /  /'

## dev: Start agent in dev mode with hot-reload UI (run in two terminals)
dev: dev-agent

## dev-agent: Start the Go agent in dev mode (auto-reloads not built-in; use air for that)
dev-agent:
	@lsof -ti :$(PORT) | xargs kill -9 2>/dev/null || true
	@lsof -ti :$$(($(PORT)+1)) | xargs kill -9 2>/dev/null || true
	go run $(LDFLAGS) ./cmd/fabric --dev --port $(PORT)

## dev-ui: Start the SvelteKit dev server (proxies API to :4892)
dev-ui:
	cd ui && npm run dev

## ui: Build SvelteKit and copy dist into the embedded path
ui:
	cd ui && npm run build
	@echo "✅ UI built to ui/dist/"

## build: Build binary for current platform (after building UI)
build: ui
	mkdir -p $(DIST)
	go build $(LDFLAGS) -o $(DIST)/$(BINARY) ./cmd/fabric
	@echo "✅ Built: $(DIST)/$(BINARY)"

## build-agent: Build agent only (without rebuilding UI)
build-agent:
	mkdir -p $(DIST)
	go build $(LDFLAGS) -o $(DIST)/$(BINARY) ./cmd/fabric

## build-all: Cross-compile for Mac/Linux/Windows/ARM
build-all: ui
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-arm64    ./cmd/fabric
	GOOS=darwin  GOARCH=amd64  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-amd64    ./cmd/fabric
	GOOS=linux   GOARCH=amd64  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-amd64     ./cmd/fabric
	GOOS=linux   GOARCH=arm64  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-arm64     ./cmd/fabric
	GOOS=linux   GOARCH=arm    GOARM=7 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-armv7 ./cmd/fabric
	GOOS=windows GOARCH=amd64  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/fabric
	@echo "✅ All platform binaries built in $(DIST)/"
	ls -lh $(DIST)/

## test: Run all Go tests
test:
	go test -v -race ./...

## lint: Run golangci-lint (install it first: brew install golangci-lint)
lint:
	golangci-lint run ./...

## clean: Remove build artifacts
clean:
	rm -rf $(DIST) ui/dist ui/.svelte-kit

## release: Tag and release via GoReleaser
release:
	goreleaser release --clean
