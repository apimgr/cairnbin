# ============================================
# Variables
# ============================================
PROJECT_NAME := $(shell git remote get-url origin 2>/dev/null | sed -E 's|.*/([^/]+)(\.git)?$$|\1|' || basename "$$(pwd)")
PROJECT_ORG  := $(shell git remote get-url origin 2>/dev/null | sed -E 's|.*/([^/]+)/[^/]+(\.git)?$$|\1|' || basename "$$(dirname "$$(pwd)")")

# Frozen at first-time setup; never changes even if the project is later
# renamed. This project has never been renamed, so it currently equals
# PROJECT_NAME - used for temp-dir naming so cleanup keeps working across
# future renames.
INTERNAL_NAME := $(PROJECT_NAME)

# Version precedence: release.txt (wins if it exists) > VERSION env var > "devel" fallback
VERSION := $(shell cat release.txt 2>/dev/null || echo "$${VERSION:-devel}")
RELEASE_VERSION := $(patsubst v%,%,$(VERSION))
RELEASE_TAG := v$(RELEASE_VERSION)

# Build info - BUILD_EPOCH is the single captured build time (Unix seconds,
# UTC); BuildDate is derived from it at process start, not embedded directly.
COMMIT_ID   := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo "N/A")
BUILD_EPOCH := $(shell date -u +%s)
BUILD_DATE  := $(shell date -u -d @$(BUILD_EPOCH) +"%Y-%m-%dT%H:%M:%SZ")

# Official site URL (OPTIONAL - never guess or assume)
# Sources (in order of precedence):
#   1. File: site.txt in project root (single line, URL only)
#   2. Environment variable: OFFICIAL_SITE=https://example.com
#   3. Empty (self-hosted projects - users must use --server flag)
OFFICIAL_SITE := $(shell [ -f site.txt ] && cat site.txt || echo "$${OFFICIAL_SITE:-}")

# Linker flags to embed build info
LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.CommitID=$(COMMIT_ID)' \
	-X 'main.BuildEpoch=$(BUILD_EPOCH)' \
	-X 'main.OfficialSite=$(OFFICIAL_SITE)'

# Directories
BINDIR := binaries
RELDIR := releases

# Build matrix — all 8 required platforms (PART 7); comma-separated, split for the shell loop
PLATFORMS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64,windows/arm64,freebsd/amd64,freebsd/arm64

# Go cache bind-mounted from host so modules are cached across builds (PART 25)
GO_CACHE  ?= $(HOME)/go/pkg/mod
GO_BUILD  ?= $(HOME)/.cache/go-build/$(PROJECT_NAME)

# Docker (PART 25)
REGISTRY  ?= ghcr.io/$(PROJECT_ORG)/$(PROJECT_NAME)

# Build container resource caps
DOCKER_MEM  ?= 4g
DOCKER_CPUS ?= 2

# Extra `docker run` flags forwarded to the toolchain container (e.g. EXTRA_ENV=-e FOO=bar)
EXTRA_ENV ?=

# `make test` knobs — defaults reproduce the standard full-suite run; tests/*.sh
# override them so no script ever needs a raw `docker run ... go test`.
TEST_TAGS     ?=
TEST_PKGS     ?= ./...
TEST_RUN      ?=
TEST_COVERAGE ?= 1
TEST_NETWORK  ?= $(PROJECT_NAME)-test-net

# GO_DOCKER_RUN is the shared docker run prefix with no image, so targets can add mounts before the image.
GO_DOCKER_RUN := docker run --rm --name $(PROJECT_NAME)-$$(tr -dc 'a-z0-9' </dev/urandom | head -c8) --memory=$(DOCKER_MEM) --cpus=$(DOCKER_CPUS) -v $(PWD):/app -v $(GO_CACHE):/usr/local/share/go/pkg/mod -v $(GO_BUILD):/usr/local/share/go/cache -w /app -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false $(EXTRA_ENV)
GO_DOCKER := $(GO_DOCKER_RUN) casjaysdev/go:latest

.PHONY: build local release docker test dev clean
.DEFAULT_GOAL := build

# =============================================================================
# BUILD - Full release build for all platforms (via Docker)
# =============================================================================
build: clean
	@mkdir -p $(BINDIR) $(GO_CACHE) $(GO_BUILD)
	@echo "Building $(PROJECT_NAME) $(VERSION) for all platforms..."
	@$(GO_DOCKER) go mod tidy
	@$(GO_DOCKER) go mod download

	@echo "Building local binary..."
	@$(GO_DOCKER) sh -c "GOOS=\$$(go env GOOS) GOARCH=\$$(go env GOARCH) \
		go build -buildvcs=false -trimpath -ldflags \"$(LDFLAGS)\" -o $(BINDIR)/$(PROJECT_NAME) ./src"

	@for platform in $$(echo "$(PLATFORMS)" | tr ',' ' '); do \
		OS=$${platform%/*}; \
		ARCH=$${platform#*/}; \
		OUTPUT=$(BINDIR)/$(PROJECT_NAME)-$$OS-$$ARCH; \
		[ "$$OS" = "windows" ] && OUTPUT=$$OUTPUT.exe; \
		echo "  → server $$OS/$$ARCH"; \
		$(GO_DOCKER) sh -c "GOOS=$$OS GOARCH=$$ARCH \
			go build -buildvcs=false -trimpath -ldflags \"$(LDFLAGS)\" \
			-o $$OUTPUT ./src" || exit 1; \
	done

	@if [ -d "src/client" ]; then \
		for platform in $$(echo "$(PLATFORMS)" | tr ',' ' '); do \
			OS=$${platform%/*}; \
			ARCH=$${platform#*/}; \
			OUTPUT=$(BINDIR)/$(PROJECT_NAME)-cli-$$OS-$$ARCH; \
			[ "$$OS" = "windows" ] && OUTPUT=$$OUTPUT.exe; \
			echo "  → cli $$OS/$$ARCH"; \
			$(GO_DOCKER) sh -c "GOOS=$$OS GOARCH=$$ARCH \
				go build -buildvcs=false -trimpath -ldflags \"$(LDFLAGS)\" \
				-o $$OUTPUT ./src/client" || exit 1; \
		done; \
	fi

	@echo ""
	@echo "✓ Built $(PROJECT_NAME) $(VERSION)"
	@echo "  Binaries: $$(ls -1 $(BINDIR)/ | wc -l | tr -d ' ') files in $(BINDIR)/"

# =============================================================================
# LOCAL - Fast host-platform build into binaries/ (production test build)
# =============================================================================
local: clean
	@mkdir -p $(BINDIR) $(GO_CACHE) $(GO_BUILD)
	@echo "Building local binaries version $(VERSION)..."
	@$(GO_DOCKER) go mod tidy
	@$(GO_DOCKER) go mod download

	@echo "Building $(PROJECT_NAME)..."
	@$(GO_DOCKER) sh -c "GOOS=\$$(go env GOOS) GOARCH=\$$(go env GOARCH) \
		go build -buildvcs=false -trimpath -ldflags \"$(LDFLAGS)\" -o $(BINDIR)/$(PROJECT_NAME) ./src"

	@if [ -d "src/client" ]; then \
		echo "Building $(PROJECT_NAME)-cli..."; \
		$(GO_DOCKER) sh -c "GOOS=\$$(go env GOOS) GOARCH=\$$(go env GOARCH) \
			go build -buildvcs=false -trimpath -ldflags \"$(LDFLAGS)\" -o $(BINDIR)/$(PROJECT_NAME)-cli ./src/client"; \
	fi

	@echo "✓ Local build complete: $(BINDIR)/"

# =============================================================================
# RELEASE - Manual local release (stable only)
# =============================================================================
release: build
	@if ! printf '%s\n' "$(VERSION)" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "ERROR: make release requires a stable SemVer version (X.Y.Z) in release.txt or VERSION"; \
		exit 1; \
	fi
	@mkdir -p $(RELDIR)
	@echo "Preparing release $(RELEASE_TAG)..."

	@echo "$(RELEASE_VERSION)" > $(RELDIR)/version.txt

	@for f in $(BINDIR)/$(PROJECT_NAME)-*; do \
		[ -f "$$f" ] || continue; \
		strip "$$f" 2>/dev/null || true; \
		cp "$$f" $(RELDIR)/; \
	done

	@tar --exclude='.git' --exclude='.github' --exclude='.gitea' \
		--exclude='$(BINDIR)' --exclude='$(RELDIR)' --exclude='*.tar.gz' \
		-czf $(RELDIR)/$(PROJECT_NAME)-$(VERSION)-source.tar.gz .

	@cd $(RELDIR) && FILES="$$(ls)" && sha256sum $$FILES > sha256.txt && sha512sum $$FILES > sha512.txt

	@gh release delete $(RELEASE_TAG) --yes 2>/dev/null || true
	@git tag -d $(RELEASE_TAG) 2>/dev/null || true
	@git push origin :refs/tags/$(RELEASE_TAG) 2>/dev/null || true

	@gh release create $(RELEASE_TAG) $(RELDIR)/* \
		--title "$(PROJECT_NAME) $(RELEASE_TAG)" \
		--notes "Release $(RELEASE_TAG)" \
		--latest

	@echo "✓ Release $(VERSION) created"

# =============================================================================
# DOCKER - Build and push multi-arch container image to $(REGISTRY)
# =============================================================================
docker:
	@echo "Building Docker image $(VERSION)..."
	@docker buildx version > /dev/null 2>&1 || (echo "docker buildx required" && exit 1)
	@docker buildx create --name $(PROJECT_NAME)-builder --use 2>/dev/null || \
		docker buildx use $(PROJECT_NAME)-builder
	@docker buildx build \
		-f docker/Dockerfile \
		--platform linux/amd64,linux/arm64 \
		--push \
		--build-arg VERSION="$(VERSION)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg BUILD_EPOCH="$(BUILD_EPOCH)" \
		--build-arg COMMIT_ID="$(COMMIT_ID)" \
		-t $(REGISTRY):$(VERSION) \
		-t $(REGISTRY):latest \
		.
	@echo "✓ Docker build complete: $(REGISTRY):$(VERSION)"

# =============================================================================
# TEST - Run all tests with ≥60% coverage enforcement (via Docker)
# =============================================================================
# Coverage gates by project type:
#   - SERVER template projects: 60% minimum (go test -cover must report >= 60.0%)
#   - All other Go projects: 60% minimum; override upward in IDEA.md
#     (## Project variables -> coverage_minimum: 80) when appropriate.
# =============================================================================
# Override knobs (used by tests/*.sh so they never bypass `make test`):
#   TEST_TAGS="-tags e2e"  TEST_PKGS="./tests/e2e/..."  TEST_RUN="-run TestTier1"
#   TEST_COVERAGE=0        skip the 60% gate (e2e suites measure integration, not unit coverage)
test:
	@mkdir -p $(GO_CACHE) $(GO_BUILD)
	@CAIRNBIN_TEST_NETWORK="$(TEST_NETWORK)"; \
	CAIRNBIN_TEST_NETWORK_CREATED=0; \
	if ! docker network inspect "$$CAIRNBIN_TEST_NETWORK" >/dev/null 2>&1; then \
		docker network create "$$CAIRNBIN_TEST_NETWORK" >/dev/null || exit 1; \
		CAIRNBIN_TEST_NETWORK_CREATED=1; \
	fi; \
	cleanup() { if [ "$$CAIRNBIN_TEST_NETWORK_CREATED" = 1 ]; then docker network rm "$$CAIRNBIN_TEST_NETWORK" >/dev/null; fi; }; \
	trap cleanup EXIT; \
	echo "Running tests$(if $(TEST_TAGS), with $(TEST_TAGS)) for $(TEST_PKGS)..."; \
	$(GO_DOCKER_RUN) --network "$$CAIRNBIN_TEST_NETWORK" casjaysdev/go:latest sh -c " \
		mkdir -p \"\$${TMPDIR:-/tmp}/$(PROJECT_ORG)\" && \
		COVDIR=\$$(mktemp -d \"\$${TMPDIR:-/tmp}/$(PROJECT_ORG)/$(INTERNAL_NAME)-XXXXXX\") && \
		go mod download && \
		mkdir -p \"\$$COVDIR/tmp\" && export TMPDIR=\"\$$COVDIR/tmp\" && \
		if [ '$(TEST_COVERAGE)' = '1' ]; then \
			go test -v $(TEST_TAGS) $(TEST_RUN) -cover -coverprofile=\$$COVDIR/coverage.out $(TEST_PKGS) && \
			COVERAGE=\$$(go tool cover -func=\$$COVDIR/coverage.out | grep total | awk '{print \$$3}' | sed 's/%//') && \
			echo \"Coverage: \$$COVERAGE%\" && \
			if [ \$$(echo \"\$$COVERAGE < 60\" | bc -l) -eq 1 ]; then \
				echo \"ERROR: Coverage is \$$COVERAGE%, must be >= 60%\"; exit 1; \
			fi && \
			echo \"Tests complete - Coverage: \$$COVERAGE% (>= 60% required) ✓\"; \
		else \
			go test -v $(TEST_TAGS) $(TEST_RUN) $(TEST_PKGS); \
		fi"

# =============================================================================
# DEV - Quick build for local development (random temp dir, no version info)
# =============================================================================
dev:
	@mkdir -p $(GO_CACHE) $(GO_BUILD)
	@$(GO_DOCKER) go mod tidy
	@mkdir -p "$${TMPDIR:-/tmp}/$(PROJECT_ORG)" && \
		BUILD_DIR=$$(mktemp -d "$${TMPDIR:-/tmp}/$(PROJECT_ORG)/$(INTERNAL_NAME)-XXXXXX") && \
		echo "Quick dev build to $$BUILD_DIR..." && \
		$(GO_DOCKER_RUN) -v $$BUILD_DIR:/build casjaysdev/go:latest \
			go build -buildvcs=false -o /build/$(PROJECT_NAME) ./src && \
		echo "Built: $$BUILD_DIR/$(PROJECT_NAME)" && \
		if [ -d "src/client" ]; then \
			$(GO_DOCKER_RUN) -v $$BUILD_DIR:/build casjaysdev/go:latest \
				go build -buildvcs=false -o /build/$(PROJECT_NAME)-cli ./src/client && \
			echo "Built: $$BUILD_DIR/$(PROJECT_NAME)-cli"; \
		fi && \
		echo "Test:  docker run --rm --name $(PROJECT_NAME)-test -v $$BUILD_DIR:/app alpine:latest /app/$(PROJECT_NAME) --help"

# =============================================================================
# CLEAN - Remove build artifacts
# =============================================================================
clean:
	@rm -rf $(BINDIR) $(RELDIR)
