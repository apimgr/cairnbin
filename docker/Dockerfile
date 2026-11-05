# =============================================================================
# Build Stage - Compile Go binary
# =============================================================================
FROM casjaysdev/go:latest AS builder

# Install git and bash (git: required for go mod download; bash: for build scripts)
RUN apk add --no-cache git bash

ARG TARGETARCH
ARG VERSION=dev
ARG BUILD_EPOCH
ARG COMMIT_ID
ARG OFFICIAL_SITE
ARG BUILD_TAGS

WORKDIR /app

# Copy go.mod first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build server binary
COPY . .
RUN mkdir -p /app/binary && \
    set --; \
    if [ -n "${BUILD_TAGS}" ]; then set -- -tags "${BUILD_TAGS}"; fi; \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build "$@" \
    -buildvcs=false -trimpath \
    -ldflags "-s -w -X 'main.Version=${VERSION}' -X 'main.CommitID=${COMMIT_ID}' -X 'main.BuildEpoch=${BUILD_EPOCH}' -X 'main.OfficialSite=${OFFICIAL_SITE}'" \
    -o /app/binary/cairnbin ./src

# Build client binary if src/client/ exists
RUN if [ -d ./src/client ]; then \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
        -buildvcs=false -trimpath \
        -ldflags "-s -w -X 'main.Version=${VERSION}' -X 'main.CommitID=${COMMIT_ID}' -X 'main.BuildEpoch=${BUILD_EPOCH}' -X 'main.OfficialSite=${OFFICIAL_SITE}'" \
        -o /app/binary/cairnbin-cli ./src/client; \
fi

# =============================================================================
# Runtime Stage - Minimal Alpine image
# All OCI image metadata is applied at build time via --annotation flags
# (docker buildx build --annotation ...) -- never via LABEL statements.
# =============================================================================
FROM alpine:latest

# ARGs for build-time values (set by docker build --build-arg)
# BUILD_DATE is passed here only because org.opencontainers.image.created
# annotations are set from this value at build time
ARG VERSION=dev
ARG BUILD_DATE
ARG BUILD_EPOCH
ARG COMMIT_ID
ARG LICENSE=MIT

# Required runtime packages per PART 26: git, curl, bash, tini, tor
# apk upgrade pulls latest security patches (e.g. openssl/libcrypto3) for
# the base image at build time, not just the packages added below
# NOTE: Tor binary installed but NOT configured here - binary handles all Tor setup (see PART 31)
RUN apk upgrade --no-cache && \
    apk add --no-cache \
    git \
    curl \
    bash \
    shadow \
    tini \
    tor

# NO directory creation - binary handles ALL setup based on env vars
# This allows users to override CONFIG_DIR, DATA_DIR, etc.
# Docker volume mounts auto-create mount points

# Copy all built binaries from builder stage (cairnbin and cairnbin-cli if built)
COPY --from=builder /app/binary/ /usr/local/bin/

# Copy BUILD-TIME overlay (entrypoint.sh) from docker/rootfs/ into image
COPY docker/rootfs/ /

# Copy Dockerfile to image (for reference and backup)
COPY docker/Dockerfile /root/Dockerfile

# Make all binaries/scripts in /usr/local/bin executable (755)
RUN chmod 755 /usr/local/bin/*

# Set environment
# Note: MODE is never baked into the image — binary defaults to production; compose sets MODE explicitly (PART 26)
# Do NOT set ENABLE_TOR here; the binary auto-detects Tor from PATH.
# DATABASE_DIR: SQLite location (binary auto-detects container, but explicit is clearer)
ENV TZ=America/New_York \
    DATABASE_DIR=/data/db/sqlite

# Internal port (always 80 in container per PART 8 / PART 26)
EXPOSE 80
# termbin/fiche raw-TCP compat listener (only binds when TERMBIN_ENABLED is truthy)
EXPOSE 9999

# Stop signal for graceful shutdown
STOPSIGNAL SIGRTMIN+3

# Health check (long start period for services that need initialization)
HEALTHCHECK --start-period=10m --interval=5m --timeout=15s --retries=3 \
    CMD /usr/local/bin/cairnbin --status || exit 1

# Use tini as init with signal propagation
# -p SIGTERM: propagate SIGTERM to child processes
ENTRYPOINT [ "tini", "-p", "SIGTERM", "--", "/usr/local/bin/entrypoint.sh" ]
