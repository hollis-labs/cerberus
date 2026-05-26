.PHONY: all build homebrew-install go-install uninstall test lint lint-go typecheck ui-build ui-dev package-release release-beta clean

GO_PACKAGES := ./cmd/cerberus ./internal/... ./pkg/...
GO_LINT_CACHE_DIR := /tmp/cerberus-go-build
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.buildDate=$(BUILD_DATE)'

all: ui-build build

ui-build:
	cd web && npm install && npm run build

ui-dev:
	cd web && npm install && npm run dev

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/cerberus ./cmd/cerberus

# `homebrew-install` mirrors the BSD/GNU install convention but is named so
# cerberus's resource-deploy pipeline doesn't auto-trigger it. Cerberus's
# RunInstall probes `make -q install` post-build and runs it when present;
# a bare `install:` target here would try to write to /usr/local/bin without
# sudo and fail every deploy. End users / source-build flows call this
# target explicitly: `make homebrew-install PREFIX=$HOME/.local`. The
# published Homebrew formula does not use this target — it installs the
# pre-built binary from the release tarball via `bin.install "cerberus"`.
homebrew-install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 bin/cerberus $(DESTDIR)$(BINDIR)/cerberus

# `make go-install` uses Go's tooling. Mirrors `go install ./cmd/cerberus`.
go-install:
	go install -ldflags "$(LDFLAGS)" ./cmd/cerberus

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/cerberus

test:
	go test $(GO_PACKAGES)

lint: lint-go

lint-go:
	mkdir -p $(GO_LINT_CACHE_DIR)
	GOCACHE=$(GO_LINT_CACHE_DIR) go vet $(GO_PACKAGES)
	GOCACHE=$(GO_LINT_CACHE_DIR) golangci-lint run --max-issues-per-linter=0 --max-same-issues=0
	GOCACHE=$(GO_LINT_CACHE_DIR) staticcheck $(GO_PACKAGES)
	GOCACHE=$(GO_LINT_CACHE_DIR) errcheck $(GO_PACKAGES)
	GOCACHE=$(GO_LINT_CACHE_DIR) govulncheck $(GO_PACKAGES)

typecheck:
	cd web && npm run typecheck

# `package-release` builds multi-arch release tarballs + checksums for upload.
# `release-beta` is the legacy alias still referenced in docs.
package-release release-beta:
	VERSION=$(VERSION) BUILD_DATE=$(BUILD_DATE) ./scripts/release-beta.sh

clean:
	rm -rf bin
	rm -f cerberus
	rm -rf web/node_modules
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
