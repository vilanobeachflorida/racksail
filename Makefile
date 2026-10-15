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
# renovate: datasource=go depName=honnef.co/go/tools
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
	@echo ""
	@echo "make release     build everything a release publishes into $(DIST)/; needs VERSION"
	@echo "make deb         build the Debian packages into $(DIST)/"
	@echo "make iso         build the installer ISO in a Debian container; ISO_ARCH, ISO_VARIANT"
	@echo "make e2e         install the ISO on a QEMU machine and run the end-to-end suites; ISO, E2E_FLAGS"
	@echo "make e2e-local   run the end-to-end suites against racksail dev on $(E2E_ADDR)"
	@echo "make cloud-image download Debian's cloud image for the end-to-end tests; CLOUD_ARCH"
	@echo "make catalog     check the catalog and build its index into $(CATALOG_OUT)"
	@echo "make licenses    check that every dependency's license is allowed"
	@echo "make vulncheck   scan the Go code and every lockfile for known vulnerabilities"
	@echo "make gen-check   fail when the generated files differ from what make gen writes"
	@echo "make sdk-check   test the add-on SDKs"

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
	rm -rf $(DIST) build sdk/ts/dist test/e2e/test-results test/e2e/playwright-report

# Delivery: release artifacts, the Debian packages, the installer ISO, the
# end-to-end tests, the catalog, and the checks CI runs beside make check.
# Tools are run with go run at pinned versions, which Renovate keeps current.

# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION := v1.8.0
# renovate: datasource=go depName=github.com/google/osv-scanner/v2
OSV_SCANNER_VERSION := v2.6.0
# renovate: datasource=go depName=github.com/goreleaser/nfpm/v2
NFPM_VERSION        := v2.47.0
# renovate: datasource=go depName=github.com/anchore/syft
SYFT_VERSION        := v1.52.0
# renovate: datasource=go depName=github.com/rhysd/actionlint
ACTIONLINT_VERSION  := v1.7.12
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
OSV_SCANNER := github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION)
NFPM        := github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
SYFT        := github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)
ACTIONLINT  := github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

DIST            := dist
RELEASE_VERSION  = $(or $(VERSION),0.0.0-dev)
RELEASE_CHANNEL  = $(or $(CHANNEL),$(shell $(GO) run ./packaging/release channel $(VERSION) 2>/dev/null))
DEB_MAINTAINER  ?= Racksail maintainers <vilanobeachflorida@users.noreply.github.com>

ISO_ARCH    ?= amd64
ISO_VARIANT ?= standard
ISO_BUILDER ?= iso/in-container.sh
ISO         ?= $(DIST)/racksail-$(RELEASE_VERSION)-$(ISO_ARCH)$(if $(filter firmware,$(ISO_VARIANT)),-firmware).iso

# Where make e2e-local runs racksail dev, apart from make dev's instance.
E2E_ADDR  ?= 127.0.0.1:7170
E2E_DATA  ?= .dev-e2e
E2E_FLAGS ?=
E2E_DEPS  := test/e2e/node_modules/.package-lock.json

# Debian's cloud image, which the end-to-end tests convert or install the
# Debian package on, checked against the checksums Debian publishes beside it.
CLOUD_ARCH  ?= amd64
CLOUD_URL   ?= https://cloud.debian.org/images/cloud/trixie/latest
CLOUD_IMAGE := build/cloud/debian-13-genericcloud-$(CLOUD_ARCH).qcow2
SHA512SUM   := $(if $(shell command -v sha512sum),sha512sum,shasum -a 512)
SDK_TS_DEPS := sdk/ts/node_modules/.package-lock.json

CATALOG_OUT ?= $(BIN)/catalog
# CI signs the index with the key in CATALOG_SIGNING_KEY, which must belong
# to catalog/keys/official.pub.
CATALOG_SIGN = $(if $(CATALOG_SIGNING_KEY),-key-env CATALOG_SIGNING_KEY -public catalog/keys/official.pub)

.PHONY: release deb sbom notices iso e2e e2e-local cloud-image catalog catalog-check catalog-publish \
	licenses vulncheck gen-check sdk-check ci-lint

check: sdk-check

$(E2E_DEPS): test/e2e/package.json test/e2e/package-lock.json
	cd test/e2e && $(NPM) ci --no-audit --no-fund
	@touch $@

$(SDK_TS_DEPS): sdk/ts/package.json sdk/ts/package-lock.json
	cd sdk/ts && $(NPM) ci --no-audit --no-fund
	@touch $@

release:
	@test -n "$(VERSION)" || { echo "make release needs VERSION, such as make release VERSION=1.2.3"; exit 1; }
	@test -n "$(RELEASE_CHANNEL)" || { echo "$(VERSION) isn't a release version"; exit 1; }
	rm -rf $(DIST)
	$(MAKE) cross VERSION=$(VERSION) CHANNEL=$(RELEASE_CHANNEL)
	$(MAKE) deb sbom VERSION=$(VERSION) CHANNEL=$(RELEASE_CHANNEL)
	cp $(BIN)/racksail-linux-amd64 $(BIN)/racksail-linux-arm64 packaging/install.sh $(DIST)/
	cp build/deb/third-party-licenses.txt $(DIST)/
	$(GO) run ./packaging/release checksums -dir $(DIST)
	@echo "$(DIST)/ holds Racksail $(VERSION) on the $(RELEASE_CHANNEL) channel, unsigned; CI signs and publishes it"

# The Debian packages, from the binaries make cross builds.
deb: notices
	@for p in $(PLATFORMS); do test -f $(BIN)/racksail-linux-$${p#*/} || { echo "run make cross first"; exit 1; }; done
	rm -rf build/deb.new
	$(GO) run ./packaging/deb -out build/deb.new -license LICENSE
	mkdir -p build/deb.new/completions
	$(GO) run ./cmd/racksail completion bash > build/deb.new/completions/racksail.bash
	$(GO) run ./cmd/racksail completion zsh > build/deb.new/completions/racksail.zsh
	$(GO) run ./cmd/racksail completion fish > build/deb.new/completions/racksail.fish
	mv build/third-party-licenses.txt build/deb.new/
	rm -rf build/deb && mv build/deb.new build/deb
	mkdir -p $(DIST)
	@# The file name keeps the release's version as it is; inside, Debian's
	@# version puts a ~ before pre-releases so they sort first.
	@for p in $(PLATFORMS); do arch=$${p#*/}; \
		echo "$(DIST)/racksail_$(RELEASE_VERSION)_$$arch.deb"; \
		VERSION=$(RELEASE_VERSION) DEB_ARCH=$$arch BINARY=$(BIN)/racksail-linux-$$arch NOTICES=build/deb/third-party-licenses.txt \
		DEB_MAINTAINER="$(DEB_MAINTAINER)" $(GO) run $(NFPM) package --config packaging/nfpm.yaml --packager deb \
			--target $(DIST)/racksail_$(RELEASE_VERSION)_$$arch.deb || exit 1; \
	done

notices: $(UI_DEPS)
	mkdir -p build
	$(GO) run ./packaging/licenses notices -out build/third-party-licenses.txt

# Software bills of materials: the Go modules in each binary, and the npm
# packages bundled into the web UI.
sbom:
	mkdir -p $(DIST)
	@for p in $(PLATFORMS); do arch=$${p#*/}; \
		$(GO) run $(SYFT) scan file:$(BIN)/racksail-linux-$$arch --source-name racksail --source-version $(RELEASE_VERSION) \
			-o spdx-json=$(DIST)/racksail-linux-$$arch.spdx.json || exit 1; \
	done
	$(GO) run $(SYFT) scan dir:ui --exclude './node_modules/**' --source-name racksail-ui --source-version $(RELEASE_VERSION) \
		-o spdx-json=$(DIST)/racksail-ui.spdx.json

# The installer ISO. Docker or Podman runs live-build in a Debian 13
# container; on Debian 13 itself, ISO_BUILDER="sudo iso/build.sh" builds
# without one.
iso:
	@test -f $(BIN)/racksail-linux-$(ISO_ARCH) || { echo "run make cross first"; exit 1; }
	$(ISO_BUILDER) --arch $(ISO_ARCH) --variant $(ISO_VARIANT) --binary $(BIN)/racksail-linux-$(ISO_ARCH) \
		--version $(RELEASE_VERSION) --out $(DIST)

# Installs the ISO on a QEMU machine and runs the suites against it; see
# test/README.md for E2E_FLAGS, such as -firmware secureboot or -disks 2.
e2e: $(E2E_DEPS)
	@test -f $(ISO) || { echo "$(ISO) doesn't exist; run make iso, or give ISO=path"; exit 1; }
	$(GO) run ./test/qemu -iso $(ISO) -version $(RELEASE_VERSION) $(E2E_FLAGS)

e2e-local: build $(E2E_DEPS)
	E2E_ADDR=$(E2E_ADDR) E2E_DATA=$(E2E_DATA) test/e2e/local.sh $(BIN)/racksail

cloud-image:
	mkdir -p $(dir $(CLOUD_IMAGE))
	curl --fail --silent --show-error --location --output $(dir $(CLOUD_IMAGE))SHA512SUMS $(CLOUD_URL)/SHA512SUMS
	curl --fail --silent --show-error --location --output $(CLOUD_IMAGE) $(CLOUD_URL)/$(notdir $(CLOUD_IMAGE))
	cd $(dir $(CLOUD_IMAGE)) && grep ' $(notdir $(CLOUD_IMAGE))$$' SHA512SUMS | $(SHA512SUM) --check

catalog-check:
	$(GO) run ./catalog/tool validate -dir catalog

catalog:
	rm -rf $(CATALOG_OUT) $(BIN)/catalog.tar.gz
	$(GO) run ./catalog/tool index -dir catalog -out $(CATALOG_OUT) -archive $(BIN)/catalog.tar.gz $(CATALOG_SIGN)

catalog-publish:
	@test -n "$(CATALOG_SIGNING_KEY)" || { echo "publishing signs the index with CATALOG_SIGNING_KEY, which isn't set"; exit 1; }
	@test -f catalog/keys/official.pub || { echo "catalog/keys/official.pub is missing; see catalog/keys/README.md"; exit 1; }
	$(MAKE) catalog
	$(GO) run ./catalog/tool verify -dir $(CATALOG_OUT) -public catalog/keys/official.pub

licenses:
	$(GO) run ./packaging/licenses check

# Advisories that don't affect Racksail are listed, with their reasons, in
# packaging/osv-scanner.toml.
vulncheck:
	$(GO) run $(GOVULNCHECK) ./...
	cd sdk/go && $(GO) run $(GOVULNCHECK) ./...
	$(GO) run $(OSV_SCANNER) scan source --config packaging/osv-scanner.toml $(foreach f,$(shell git ls-files '*go.mod' '*package-lock.json' | grep -v '^\.'),--lockfile $(f))

# make gen rewrites the generated files; any difference means they were
# committed out of date.
gen-check: gen
	@git diff --exit-code -- spec ui/src/lib/api/schema.d.ts ui/src/design/theme.css || \
		{ echo "the generated files above are out of date; run make gen and commit them"; exit 1; }

sdk-check: $(SDK_TS_DEPS)
	cd sdk/go && $(GO) vet ./... && $(GO) run $(STATICCHECK) ./... && $(GO) test ./...
	cd sdk/ts && $(NPM) run typecheck && $(NPM) test
	cd sdk/python && PYTHONPATH=src python3 -B -m unittest discover -s tests

# Lints the workflows and the shell scripts; CI runs it, with shellcheck.
ci-lint:
	$(GO) run $(ACTIONLINT)
	shellcheck $$(git ls-files '*.sh' iso/auto/config 'iso/config/hooks/*' | grep -v '^\.')
