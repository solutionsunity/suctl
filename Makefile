# suctl/Makefile — single build entry point for the suctl product.
#
# Artifacts are segregated per target platform so Linux and Windows builds
# never overwrite each other:
#
#   bin/$(GOOS)-$(GOARCH)/suctl[.exe]                     → suctl binary
#   bin/$(GOOS)-$(GOARCH)/modules/suctl-mod-{name}/...     → modules
#
# Defaults to the host platform; cross-build by overriding GOOS/GOARCH, e.g.:
#   make all                              # host (e.g. linux-amd64)
#   make all GOOS=windows GOARCH=amd64    # lands in bin/windows-amd64/
#
# To deploy (Linux):
#   make all
#   cd bin/linux-amd64 && sudo ./suctl install

GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
PLATFORM := $(GOOS)-$(GOARCH)
export GOOS
export GOARCH

# Pure-Go build (no libsystemd/CGo): fully static binaries with no glibc floor,
# so the same artifact runs across glibc versions (and musl). Keep CGo off.
CGO_ENABLED ?= 0
export CGO_ENABLED

# Executable suffix and platform-scoped output tree. Artifacts land in the repo's
# own bin/$(PLATFORM)/ — one sub-tree per target platform, so Linux and Windows
# builds never clobber each other.
EXE := $(if $(filter windows,$(GOOS)),.exe,)
BIN := bin/$(PLATFORM)
GO  := go
CMD := ./cmd

# Version is canonical in internal/version/VERSION (file-as-truth) and is embedded
# into the binary at compile time via //go:embed, so no -ldflags stamping is
# needed. Local builds simply embed whatever the file says — no tag required. The
# git tag and the release are created downstream by the GitHub Action from this
# same file (the file change is the trigger), so no manual tagging is needed to
# build.
VERSION := $(shell cat internal/version/VERSION 2>/dev/null)

# No external C library deps: the build is pure Go (CGo disabled).
DEPS :=

# Per-platform module set. Linux ships all bundled modules; no module compiles
# for Windows yet, so its set is empty until a Windows-capable module exists.
MODULES_linux   := mod-nginx mod-certbot mod-os mod-fail2ban mod-odoo
MODULES_windows :=
MODULES := $(MODULES_$(GOOS))

.PHONY: all _build suctl-bin suctl-modtest mod-nginx mod-certbot mod-os mod-fail2ban mod-odoo \
        vet test test-py check clean clean-all help

## all             clean target platform + build suctl + suctl-modtest + modules
##                 → bin/$(PLATFORM)/  (only the current platform tree is
##                 wiped first, so other platforms' artifacts are never clobbered)
all: clean
	@$(MAKE) --no-print-directory _build

_build: suctl-bin suctl-modtest $(MODULES)

## suctl-bin       compile the suctl binary → $(BIN)/suctl$(EXE)
suctl-bin: $(DEPS)
	@test -n "$(VERSION)" || { printf '\nERROR: could not read internal/version/VERSION.\n\n' >&2; exit 1; }
	@echo "→ suctl$(EXE) ($(VERSION))"
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/suctl$(EXE) $(CMD)

## suctl-modtest   compile the module BIST tester → $(BIN)/suctl-modtest$(EXE)
suctl-modtest: $(DEPS)
	@echo "→ suctl-modtest$(EXE)"
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/suctl-modtest$(EXE) ./cmd/modtest

## mod-nginx       compile suctl-mod-nginx → $(BIN)/modules/suctl-mod-nginx/
mod-nginx:
	@echo "→ suctl-mod-nginx$(EXE)"
	@mkdir -p $(BIN)/modules/suctl-mod-nginx
	cd modules/suctl-mod-nginx && $(GO) build -o ../../$(BIN)/modules/suctl-mod-nginx/suctl-mod-nginx$(EXE) .
	cp modules/suctl-mod-nginx/manifest.json $(BIN)/modules/suctl-mod-nginx/
	cp modules/suctl-mod-nginx/surface.json  $(BIN)/modules/suctl-mod-nginx/

## mod-certbot     compile suctl-mod-certbot → $(BIN)/modules/suctl-mod-certbot/
mod-certbot:
	@echo "→ suctl-mod-certbot$(EXE)"
	@mkdir -p $(BIN)/modules/suctl-mod-certbot
	cd modules/suctl-mod-certbot && $(GO) build -o ../../$(BIN)/modules/suctl-mod-certbot/suctl-mod-certbot$(EXE) .
	cp modules/suctl-mod-certbot/manifest.json $(BIN)/modules/suctl-mod-certbot/

## mod-os          compile suctl-mod-os → $(BIN)/modules/suctl-mod-os/
mod-os:
	@echo "→ suctl-mod-os$(EXE)"
	@mkdir -p $(BIN)/modules/suctl-mod-os
	cd modules/suctl-mod-os && $(GO) build -o ../../$(BIN)/modules/suctl-mod-os/suctl-mod-os$(EXE) .
	cp modules/suctl-mod-os/manifest.json $(BIN)/modules/suctl-mod-os/
	cp modules/suctl-mod-os/surface.json  $(BIN)/modules/suctl-mod-os/



## mod-fail2ban    stage suctl-mod-fail2ban → $(BIN)/modules/suctl-mod-fail2ban/
##                 (Python entrypoint + vendored SDK + filter assets + catalog)
mod-fail2ban:
	@echo "→ suctl-mod-fail2ban"
	@mkdir -p $(BIN)/modules/suctl-mod-fail2ban/filter.d
	cp modules/suctl-mod-fail2ban/suctl-mod-fail2ban $(BIN)/modules/suctl-mod-fail2ban/
	cp modules/suctl-mod-fail2ban/manifest.json      $(BIN)/modules/suctl-mod-fail2ban/
	cp modules/suctl-mod-fail2ban/surface.json       $(BIN)/modules/suctl-mod-fail2ban/
	cp modules/suctl-mod-fail2ban/catalog.json       $(BIN)/modules/suctl-mod-fail2ban/
	cp sdk/python/suctlmod.py                        $(BIN)/modules/suctl-mod-fail2ban/
	cp modules/suctl-mod-fail2ban/filter.d/*.conf    $(BIN)/modules/suctl-mod-fail2ban/filter.d/

## mod-odoo        stage suctl-mod-odoo → $(BIN)/modules/suctl-mod-odoo/
##                 (vendors sdk/python/suctlmod.py alongside the entrypoint)
mod-odoo:
	@echo "→ suctl-mod-odoo"
	@mkdir -p $(BIN)/modules/suctl-mod-odoo/hooks
	cp modules/suctl-mod-odoo/suctl-mod-odoo      $(BIN)/modules/suctl-mod-odoo/
	cp modules/suctl-mod-odoo/suctl-odoo-service   $(BIN)/modules/suctl-mod-odoo/
	cp modules/suctl-mod-odoo/manifest.json        $(BIN)/modules/suctl-mod-odoo/
	cp modules/suctl-mod-odoo/surface.json         $(BIN)/modules/suctl-mod-odoo/
	cp sdk/python/suctlmod.py                      $(BIN)/modules/suctl-mod-odoo/
	cp modules/suctl-mod-odoo/hooks/*.sh           $(BIN)/modules/suctl-mod-odoo/hooks/
	cp modules/suctl-mod-odoo/hooks/*.py           $(BIN)/modules/suctl-mod-odoo/hooks/

## vet             run go vet
vet:
	$(GO) vet ./...

## test            run all Go tests
test:
	$(GO) test ./...

## test-py          run Python module unit tests (pytest; integration excluded —
##                  those need a live socket, run separately with -m integration)
test-py:
	cd internal/installer && python3 -m pytest -q -m "not integration"
	cd modules/suctl-mod-fail2ban && python3 -m pytest -q

## check           vet + test + test-py — run before every commit
check: vet test test-py

## clean           remove only the current platform tree ($(BIN))
clean:
	rm -rf $(BIN)

## clean-all       remove every platform tree (bin/)
clean-all:
	rm -rf bin

## help            list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
