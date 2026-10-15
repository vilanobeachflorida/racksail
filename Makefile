# Build, check and run Racksail. `make help` lists the targets.
#
# Everything here looks only at files tracked by git or at Go package
# patterns, so other checkouts or worktrees inside the repository directory
# are never picked up.

GO          ?= go
NPM         ?= npm
BIN         := bin

# Where `make dev` listens and keeps its state. Give each checkout its own
# when running several at once.
DEV_ADDR       ?= 127.0.0.1:7070
DEV_EDGE_HTTP  ?= 127.0.0.1:7080
DEV_EDGE_HTTPS ?= 127.0.0.1:7443
DEV_DATA       ?= .dev

# Release builds set these, for example `make cross VERSION=1.2.0 CHANNEL=stable`,
# and then also record the commit and when it was made. A binary carries no
# build time of its own, so building the same commit twice gives the same
# binary. Without VERSION the binary reports 0.0.0-dev on the dev channel, and
# the commit and its time come from Go's own version control stamping.
VERSION     ?=
CHANNEL     ?=
COMMIT      := $(if $(VERSION),$(shell git rev-parse HEAD 2>/dev/null))
COMMIT_TIME := $(if $(VERSION),$(shell TZ=UTC0 git log -1 --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd 2>/dev/null))

# go run builds the pinned version with the current toolchain and caches the
# build, so a Go upgrade never leaves a stale binary behind.
STATICCHECK_VERSION := v0.8.2
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

# The product is pure Go, so it cross-compiles anywhere.
export CGO_ENABLED := 0
export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD := 1
export ASTRO_TELEMETRY_DISABLED := 1

UPDATES     := github.com/vilanobeachflorida/racksail/internal/updates
LDFLAGS     := -s -w $(if $(VERSION),-X $(UPDATES).version=$(VERSION)) $(if $(CHANNEL),-X $(UPDATES).channel=$(CHANNEL)) \
	$(if $(COMMIT),-X $(UPDATES).commit=$(COMMIT)) $(if $(COMMIT_TIME),-X $(UPDATES).commitTime=$(COMMIT_TIME))
GO_FILES     = $(shell git ls-files '*.go')
PLATFORMS   := linux/amd64 linux/arm64

UI_DEPS     := ui/node_modules/.package-lock.json
DOCS_DEPS   := docs/site/node_modules/.package-lock.json

.PHONY: all help build ui cross check fmt-check vet staticcheck build-check lint test go-test \
	ui-test test-race ui-check gen fmt dev docs clean

all: build

help:
	@echo "make build       build the UI and bin/racksail for this machine"
	@echo "make cross       build release binaries for $(PLATFORMS)"
	@echo "make check       what a pull request must pass: format, vet, staticcheck, builds, tests, UI;"
	@echo "                 CI also runs make test-race and make docs"
	@echo "make lint        format check, go vet, staticcheck and the UI linters"
	@echo "make test        Go and UI tests"
	@echo "make test-race   Go tests with the race detector (needs a C compiler)"
	@echo "make gen         regenerate the OpenAPI documents in spec/ and the UI's generated files"
	@echo "make fmt         format Go and UI code"
	@echo "make dev         run racksail dev on $(DEV_ADDR) and the UI dev server with hot reload;"
	@echo "                 DEV_ADDR, DEV_EDGE_HTTP, DEV_EDGE_HTTPS and DEV_DATA choose where"
	@echo "make docs        build the documentation site"
	@echo "make clean       remove build output"

$(UI_DEPS): ui/package.json ui/package-lock.json
	cd ui && $(NPM) ci --no-audit --no-fund
	@touch $@

$(DOCS_DEPS): docs/site/package.json docs/site/package-lock.json
	cd docs/site && $(NPM) ci --no-audit --no-fund
	@touch $@

ui: $(UI_DEPS)
	cd ui && $(NPM) run build

build: ui
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/racksail ./cmd/racksail

cross: ui
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "$(BIN)/racksail-$$os-$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/racksail-$$os-$$arch ./cmd/racksail || exit 1; \
	done

check: fmt-check vet staticcheck build-check go-test ui-check

# Files deleted in the working tree are still tracked until committed.
fmt-check:
	@files=$$(for f in $(GO_FILES); do [ -f "$$f" ] && echo "$$f"; done); \
	unformatted=$$([ -z "$$files" ] || gofmt -l $$files); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

# Linux-only code sits behind build tags, so vet both sides.
vet:
	$(GO) vet ./...
	GOOS=linux $(GO) vet ./...

# staticcheck is built for this machine either way; for the Linux pass,
# -exec runs it with GOOS=linux so it analyzes the Linux side.
staticcheck:
	$(GO) run $(STATICCHECK) ./...
	$(GO) run -exec 'env GOOS=linux' $(STATICCHECK) ./...

build-check:
	GOOS=darwin $(GO) build ./...
	GOOS=linux GOARCH=amd64 $(GO) build ./...
	GOOS=linux GOARCH=arm64 $(GO) build ./...

lint: fmt-check vet staticcheck $(UI_DEPS)
	cd ui && $(NPM) run lint && $(NPM) run typecheck

test: go-test ui-test

go-test:
	$(GO) test ./...

ui-test: $(UI_DEPS)
	cd ui && $(NPM) test

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

ui-check: ui-test
	cd ui && $(NPM) run typecheck
	cd ui && $(NPM) run lint
	cd ui && $(NPM) run check:api
	cd ui && $(NPM) run build

# Generated files are committed; tests fail when they drift from the code.
gen: $(UI_DEPS)
	$(GO) run ./cmd/racksail openapi > spec/openapi.json.tmp && mv spec/openapi.json.tmp spec/openapi.json
	$(GO) run ./cmd/racksail openapi --agent > spec/agent-protocol.json.tmp && mv spec/agent-protocol.json.tmp spec/agent-protocol.json
	$(GO) run ./cmd/racksail openapi --addon > spec/addon-protocol.json.tmp && mv spec/addon-protocol.json.tmp spec/addon-protocol.json
	$(GO) run ./cmd/racksail theme css > ui/src/design/theme.css.tmp && mv ui/src/design/theme.css.tmp ui/src/design/theme.css
	$(GO) run ./brand/gen
	cd ui && $(NPM) run gen:api

fmt: $(UI_DEPS)
	@files=$$(for f in $(GO_FILES); do [ -f "$$f" ] && echo "$$f"; done); [ -z "$$files" ] || gofmt -w $$files
	cd ui && $(NPM) run format

# Stopping make stops both servers.
dev: $(UI_DEPS)
	@trap 'kill 0' EXIT INT TERM; \
	$(GO) run ./cmd/racksail dev --addr $(DEV_ADDR) --edge-http $(DEV_EDGE_HTTP) --edge-https $(DEV_EDGE_HTTPS) --data $(DEV_DATA) & \
	RACKSAIL_DEV_ADDR=$(DEV_ADDR) $(NPM) --prefix ui run dev & \
	wait

docs: $(DOCS_DEPS)
	cd docs/site && $(NPM) run build

clean:
	rm -rf $(BIN) internal/webui/dist/app docs/site/dist docs/site/.astro
