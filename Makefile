# uped build helpers. Requires Go (see go.mod) and a POSIX shell.
# Release builds in CI pass VERSION=vX.Y.Z explicitly.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LISTEN  ?= 127.0.0.1:8080
DATA    ?= ./data
ARCHES  ?= amd64 arm64

GOFLAGS_BUILD := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"
SHA256SUM := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

.PHONY: build test run e2e cross clean

## build: static binary for the host OS/arch at dist/uped
build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o dist/uped ./cmd/uped

## test: vet plus unit tests with the race detector
test:
	go vet ./...
	go test -race ./...

## run: serve locally from ./data (override with LISTEN=... DATA=...)
run:
	go run ./cmd/uped --data $(DATA) --listen $(LISTEN)

## e2e: Playwright end-to-end suite (created in Phase 4 of the plan)
e2e:
	@if [ ! -f e2e/package.json ]; then echo "e2e suite does not exist yet (Phase 4 of plans/uped-home-file-drop.md)"; exit 1; fi
	cd e2e && npm ci && PLAYWRIGHT_BROWSERS_PATH=$${PLAYWRIGHT_BROWSERS_PATH:-/opt/pw-browsers} npx playwright test

## cross: release tarballs for linux/amd64 and linux/arm64 plus checksums.txt in dist/
cross:
	rm -rf dist/cross && mkdir -p dist/cross
	rm -f dist/uped_linux_*.tar.gz dist/checksums.txt
	for arch in $(ARCHES); do \
		mkdir -p dist/cross/$$arch && \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS_BUILD) -o dist/cross/$$arch/uped ./cmd/uped && \
		tar -czf dist/uped_linux_$$arch.tar.gz -C dist/cross/$$arch uped || exit 1; \
	done
	cd dist && $(SHA256SUM) uped_linux_*.tar.gz > checksums.txt
	rm -rf dist/cross
	@cat dist/checksums.txt

clean:
	rm -rf dist
