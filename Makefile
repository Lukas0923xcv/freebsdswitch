# bsdswitch - build, test, package.
#
#   make            build web UI + binary for this host
#   make freebsd    cross-compile for FreeBSD amd64 and arm64 (build/)
#   make test       unit + API tests
#   make e2e        Playwright web UI tests (simulator, any OS)
#   make lab-up / lab-test / lab-down   integration lab (FreeBSD VM, root)
#   make pkg        FreeBSD package (on FreeBSD)
#   make iso        installer ISO/memstick (see iso/README.md)

# pkg-compatible version: v0.1.0 -> 0.1.0, v0.1.0-3-gabc -> 0.1.0.3
VERSION ?= $(or $(shell git describe --tags --match 'v*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g[0-9a-f]+$$/.\1/'),0.1.0)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/Lukas0923xcv/freebsdswitch/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
GO      ?= go
NPM     ?= npm
FREEBSD_VERSION ?= 15.1

.PHONY: all build web web-deps freebsd test vet e2e lab-up lab-run lab-test lab-down pkg pkg-arm64 iso clean dev

all: web build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o build/bsdswitch ./cmd/bsdswitch

web-deps:
	cd web && $(NPM) ci --no-audit --no-fund

web: web-deps
	cd web && $(NPM) run build

freebsd:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o build/bsdswitch-freebsd-amd64 ./cmd/bsdswitch
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o build/bsdswitch-freebsd-arm64 ./cmd/bsdswitch

vet:
	$(GO) vet ./...
	GOOS=freebsd $(GO) vet ./...
	GOOS=freebsd $(GO) vet -tags integration ./tests/integration/

test: vet
	$(GO) test -count=1 ./...

e2e: build
	cd tests/e2e && $(NPM) ci --no-audit --no-fund && BSDSWITCH_BIN=$(CURDIR)/build/bsdswitch npx playwright test

# development: daemon with simulator on http://127.0.0.1:8080; then run
# `cd web && npm run dev` (vite proxies /api to :8080)
dev: build
	./build/bsdswitch daemon -sim -config-dir .dev/etc -state .dev/db -socket .dev/api.sock -listen-http 127.0.0.1:8080

# ---- integration lab (FreeBSD 15 VM, as root) ------------------------------
lab-up: build
	sh tests/integration/lab.sh up

lab-run: build
	sh tests/integration/lab.sh run

lab-down:
	sh tests/integration/lab.sh down

lab-test: build
	$(GO) test -tags integration -count=1 -v ./tests/integration/

# ---- packaging (FreeBSD) -----------------------------------------------------
pkg: build
	sh packaging/mkpkg.sh build/bsdswitch $(VERSION) $$(uname -p) dist

pkg-arm64:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o build/bsdswitch-arm64 ./cmd/bsdswitch
	sh packaging/mkpkg.sh build/bsdswitch-arm64 $(VERSION) aarch64 dist

iso:
	sh iso/build-iso.sh -v $(FREEBSD_VERSION) -p dist/bsdswitch-$(VERSION).pkg

clean:
	rm -rf ./build ./dist ./iso/work ./.dev ./tests/e2e/.state ./tests/e2e/test-results
