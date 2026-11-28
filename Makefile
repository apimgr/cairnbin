# PROJECT_NAME and PROJECT_ORG come from IDEA.md, then the git remote, then the directory.
PROJECT_NAME := $(shell sed -n 's/^project_name:[[:space:]]*//p' IDEA.md 2>/dev/null | head -n1 | tr -d '[:space:]')
ifeq ($(PROJECT_NAME),)
PROJECT_NAME := $(shell git remote get-url origin 2>/dev/null | sed -E 's|.*/([^/]+)$$|\1|; s|\.git$$||' || basename "$$(pwd)")
endif
PROJECT_ORG := $(shell sed -n 's/^project_org:[[:space:]]*//p' IDEA.md 2>/dev/null | head -n1 | tr -d '[:space:]')
ifeq ($(PROJECT_ORG),)
PROJECT_ORG := $(shell git remote get-url origin 2>/dev/null | sed -E 's|.*/([^/]+)/[^/]+(\.git)?$$|\1|' || basename "$$(dirname "$$(pwd)")")
endif
INTERNAL_NAME := $(shell sed -n 's/^internal_name:[[:space:]]*//p' IDEA.md 2>/dev/null | head -n1 | tr -d '[:space:]')
ifeq ($(INTERNAL_NAME),)
INTERNAL_NAME := $(PROJECT_NAME)
endif
VERSION ?= $(shell v=; [ -r release.txt ] && v=$$(tr -d '[:space:]' < release.txt); if [ -n "$$v" ]; then echo "$$v"; else git rev-parse --short=7 HEAD 2>/dev/null || echo devel; fi)
BUILD_EPOCH := $(shell date -u +%s)
BUILD_DATE := $(shell date -u -d @$(BUILD_EPOCH) +"%Y-%m-%dT%H:%M:%SZ")
COMMIT_ID := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo N/A)
OFFICIAL_SITE := $(shell [ -f site.txt ] && cat site.txt || echo "$${OFFICIAL_SITE:-}")
LDFLAGS := -s -w -X 'main.Version=$(VERSION)' -X 'main.CommitID=$(COMMIT_ID)' -X 'main.BuildEpoch=$(BUILD_EPOCH)' -X 'main.OfficialSite=$(OFFICIAL_SITE)'
DEBUG_LDFLAGS := -X 'main.Version=$(VERSION)' -X 'main.CommitID=$(COMMIT_ID)' -X 'main.BuildEpoch=$(BUILD_EPOCH)' -X 'main.OfficialSite=$(OFFICIAL_SITE)'
DEBUG_GCFLAGS := all=-N -l
BINDIR ?= binaries
RELDIR ?= releases
GO_CACHE ?= $(HOME)/go/pkg/mod
GO_BUILD ?= $(HOME)/.cache/go-build/$(PROJECT_NAME)
PLATFORMS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64,windows/arm64,freebsd/amd64,freebsd/arm64
REGISTRY ?= ghcr.io/$(PROJECT_ORG)/$(PROJECT_NAME)
DOCKER_MEM ?= 4g
DOCKER_CPUS ?= 2
DOCKER_CPU_SHARES ?= 256
DOCKER_TIMEOUT ?= 3600
GO_DOCKER_RUN := timeout -k 30 $(DOCKER_TIMEOUT) docker run --rm --no-healthcheck \
	--name $(PROJECT_NAME)-$$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c1-8) \
	--memory=$(DOCKER_MEM) --cpus=$(DOCKER_CPUS) --cpu-shares=$(DOCKER_CPU_SHARES) \
	-v $(PWD):/app -v $(GO_CACHE):/usr/local/share/go/pkg/mod -v $(GO_BUILD):/usr/local/share/go/cache \
	-w /app -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false
GO_DOCKER := $(GO_DOCKER_RUN) casjaysdev/go:latest
.DEFAULT_GOAL := help
.PHONY: build release docker test clean help

build: clean
	@mkdir -p $(BINDIR) $(GO_CACHE) $(GO_BUILD)
	@echo "Building debug binaries, version $(VERSION)..."
	@$(GO_DOCKER_RUN) -e BUILD_LDFLAGS="$(DEBUG_LDFLAGS)" -e BUILD_GCFLAGS="$(DEBUG_GCFLAGS)" casjaysdev/go:latest sh -c 'set -e; go mod tidy; go mod download; \
	GOOS=$$(go env GOOS) GOARCH=$$(go env GOARCH) go build -buildvcs=false -gcflags "$$BUILD_GCFLAGS" -ldflags "$$BUILD_LDFLAGS" -o $(BINDIR)/$(PROJECT_NAME) ./src; \
	if [ -d src/client ]; then GOOS=$$(go env GOOS) GOARCH=$$(go env GOARCH) go build -buildvcs=false -gcflags "$$BUILD_GCFLAGS" -ldflags "$$BUILD_LDFLAGS" -o $(BINDIR)/$(PROJECT_NAME)-cli ./src/client; fi; \
	for platform in $$(echo "$(PLATFORMS)" | tr "," " "); do OS=$${platform%/*}; ARCH=$${platform#*/}; EXT=""; [ "$$OS" != windows ] || EXT=.exe; \
	GOOS=$$OS GOARCH=$$ARCH go build -buildvcs=false -gcflags "$$BUILD_GCFLAGS" -ldflags "$$BUILD_LDFLAGS" -o $(BINDIR)/$(PROJECT_NAME)-$$OS-$$ARCH$$EXT ./src; \
	if [ -d src/client ]; then GOOS=$$OS GOARCH=$$ARCH go build -buildvcs=false -gcflags "$$BUILD_GCFLAGS" -ldflags "$$BUILD_LDFLAGS" -o $(BINDIR)/$(PROJECT_NAME)-cli-$$OS-$$ARCH$$EXT ./src/client; fi; done'
	@echo "Build complete: $(BINDIR)/"

release:
	@mkdir -p $(RELDIR) $(GO_CACHE) $(GO_BUILD)
	@rm -rf $(RELDIR)/*
	@echo "Building production binaries, version $(VERSION)..."
	@$(GO_DOCKER_RUN) -e BUILD_LDFLAGS="$(LDFLAGS)" casjaysdev/go:latest sh -c 'set -e; go mod tidy; go mod download; \
	for platform in $$(echo "$(PLATFORMS)" | tr "," " "); do OS=$${platform%/*}; ARCH=$${platform#*/}; EXT=""; [ "$$OS" != windows ] || EXT=.exe; \
	GOOS=$$OS GOARCH=$$ARCH go build -buildvcs=false -trimpath -ldflags "$$BUILD_LDFLAGS" -o $(RELDIR)/$(PROJECT_NAME)-$$OS-$$ARCH$$EXT ./src; \
	if [ -d src/client ]; then GOOS=$$OS GOARCH=$$ARCH go build -buildvcs=false -trimpath -ldflags "$$BUILD_LDFLAGS" -o $(RELDIR)/$(PROJECT_NAME)-cli-$$OS-$$ARCH$$EXT ./src/client; fi; done'
	@echo "$(VERSION)" > $(RELDIR)/version.txt
	@tar --exclude='.git' --exclude='.github' --exclude='.gitea' --exclude='binaries' --exclude='releases' --exclude='*.tar.gz' -czf $(RELDIR)/$(PROJECT_NAME)-$(VERSION)-source.tar.gz .
	@cd $(RELDIR) && FILES="$$(ls)" && sha256sum $$FILES > sha256.txt && sha512sum $$FILES > sha512.txt
	@echo "Release build complete: $(RELDIR)/"

docker:
	@echo "Building Docker image $(VERSION)..."
	@docker buildx version >/dev/null 2>&1 || (echo "docker buildx required" && exit 1)
	@docker buildx create --name multiarch-builder --use 2>/dev/null || docker buildx use multiarch-builder
	@docker buildx build -f docker/Dockerfile --load --build-arg VERSION="$(VERSION)" --build-arg BUILD_DATE="$(BUILD_DATE)" --build-arg BUILD_EPOCH="$(BUILD_EPOCH)" --build-arg COMMIT_ID="$(COMMIT_ID)" --build-arg OFFICIAL_SITE="$(OFFICIAL_SITE)" -t $(REGISTRY):$(VERSION) -t $(REGISTRY):latest .
	@bash tests/image_check.sh $(REGISTRY):$(VERSION)
	@echo "Docker build complete: $(REGISTRY):$(VERSION)"

test:
	@mkdir -p $(GO_CACHE) $(GO_BUILD)
	@$(GO_DOCKER) sh -c 'set -e; mkdir -p "$${TMPDIR:-/tmp}/$(PROJECT_ORG)"; COVDIR=$$(mktemp -d "$${TMPDIR:-/tmp}/$(PROJECT_ORG)/$(INTERNAL_NAME)-XXXXXX"); go mod download; go vet ./...; staticcheck ./...; govulncheck ./...; if ls tests/*.sh >/dev/null 2>&1; then shellcheck tests/*.sh && shfmt -d -i 2 -ci tests/*.sh; fi; mkdir -p "$$COVDIR/data"; TMPDIR="$$COVDIR" go test -v -cover ./... -args -test.gocoverdir="$$COVDIR/data"; go tool covdata textfmt -i="$$COVDIR/data" -o="$$COVDIR/coverage.out"; COVERAGE=$$(go tool cover -func="$$COVDIR/coverage.out" | grep "^total:" | awk "{print \$$3}" | sed "s/%//"); echo "Coverage: $$COVERAGE%"; if [ $$(echo "$$COVERAGE < 60" | bc -l) -eq 1 ]; then echo "ERROR: Coverage is $$COVERAGE%, must be >= 60%"; exit 1; fi; echo "Tests complete - Coverage: $$COVERAGE% (>= 60% required) ✓"'
	@echo "Running binary checks..."
	@./tests/run_tests.sh

clean:
	@rm -rf $(BINDIR) $(RELDIR)

help:
	@echo "Targets:"
	@echo "  build    Debug binaries for all platforms to $(BINDIR)/"
	@echo "  test     Quality gates, coverage tests, and binary checks"
	@echo "  release  Production binaries, source archive, and checksums to $(RELDIR)/"
	@echo "  docker   Build and load the host-platform image locally (no push)"
	@echo "  clean    Remove $(BINDIR)/ and $(RELDIR)/"
	@echo "  help     Show this list"
