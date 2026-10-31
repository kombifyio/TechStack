# syntax=docker/dockerfile:1.28

FROM node:24.22.1-slim AS frontend
WORKDIR /build/app
# Corepack resolves pnpm from the packageManager field of app/package.json.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable
COPY app/package.json app/pnpm-lock.yaml app/pnpm-workspace.yaml app/.npmrc ./
COPY app/scripts/install-deps.mjs ./scripts/install-deps.mjs
COPY app/third_party ./third_party
RUN pnpm install --frozen-lockfile
COPY VERSION /build/VERSION
COPY app/ .
ENV PUBLIC_KOMBIFY_EDITION=selfhost-oss
RUN pnpm build

FROM golang:1.27.3-alpine AS backend
WORKDIR /build
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /build/app/build-static ./internal/frontend/dist
ARG TECHSTACK_PRODUCT_VERSION=""
ARG GIT_COMMIT=""
RUN set -eu; \
    version="${TECHSTACK_PRODUCT_VERSION:-$(tr -d '[:space:]' < VERSION)}"; \
    printf '%s' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; \
    revision="${GIT_COMMIT:-dev}"; \
    if [ "$revision" != dev ]; then printf '%s' "$revision" | grep -Eq '^[0-9a-f]{40}$'; fi; \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath \
      -tags techstack_static_ui \
      -ldflags="-s -w -X main.version=${version} -X main.buildRevision=${revision}" \
      -o /out/techstack ./cmd/techstack; \
    CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
      -o /out/guard-release-tool ./cmd/guard-release-tool

# Pre-sign source CI stops at backend. Only the numeric product release builds
# this complete runtime, after Guard has been published from that public main SHA.
FROM alpine:3.24 AS signed-runtime
RUN apk add --no-cache bash ca-certificates curl jq tar
WORKDIR /build
COPY VERSION ./VERSION
COPY --from=backend /out/guard-release-tool /usr/local/bin/guard-release-tool
COPY scripts/new-linux-stackkit-spec-templates.sh ./scripts/new-linux-stackkit-spec-templates.sh
COPY docs/examples/seeds ./docs/examples/seeds
COPY Dockerfile ./Dockerfile
ARG STACKKIT_RELEASE_TAG="v0.54.13"
ARG STACKKIT_RELEASE_KIT="cloud-kit"
ARG STACKKIT_RELEASE_BOOTSTRAP_KIT="basement-kit"
ARG STACKKIT_RELEASE_REPO="kombifyio/StackKits"
ARG STACKKIT_RELEASE_ARCHIVE_SHA256="751d404a2c600ae2a1cf57fdcfad37cd8f8567856bc032e532010f5219e4cffb"
ARG STACKKIT_RELEASE_INDEX_SHA256="e66e63c4cba043efc6f5414b59317138cb6cf8aa8065d7f8ec6f79e802bc7bc2"
ARG STACKKIT_RELEASE_COMPATIBILITY_SHA256="c3955c1a43ab2d0afe18cf2d86073ac0e3ef7f78c3f40d6ebe0ae3d11dfcbfa4"
ARG STACKKIT_RELEASE_USE_CASE_CATALOG_SHA256="96c9cfc32097eeedb3ca73f1f991acac67885c2a6556a5ce3a60cfae59126010"
ARG GIT_COMMIT=""
ARG TECHSTACK_PRODUCT_VERSION=""
RUN set -eu; \
    version="${TECHSTACK_PRODUCT_VERSION:-$(tr -d '[:space:]' < VERSION)}"; \
    test "$version" = "$(tr -d '[:space:]' < VERSION)"; \
    printf '%s' "$GIT_COMMIT" | grep -Eq '^[0-9a-f]{40}$'; \
    base="https://github.com/kombifyio/TechStack/releases/download/guard-v${version}"; \
    mkdir -p /app/guard-release; \
    for asset in release-manifest.json release-manifest.sigstore.json \
      techstack-linux-amd64 techstack-linux-amd64.sigstore.json \
      techstack-linux-arm64 techstack-linux-arm64.sigstore.json \
      stackkit-runtime-linux-amd64.tar.gz stackkit-runtime-linux-arm64.tar.gz; do \
      curl --fail --silent --show-error --location "${base}/${asset}" --output "/app/guard-release/${asset}"; \
    done; \
    guard-release-tool verify /app/guard-release --expected-source "$GIT_COMMIT" --require-runtime-bundles; \
    jq -e --arg version "$version" --arg stackkits "$STACKKIT_RELEASE_TAG" \
      '.version == $version and .stackkits.version == $stackkits' /app/guard-release/release-manifest.json >/dev/null; \
    tar --extract --gzip --file /app/guard-release/stackkit-runtime-linux-amd64.tar.gz --directory /app; \
    test "$(jq -er '.version' /app/.stackkit/stackkits-release-pin.json)" = "$STACKKIT_RELEASE_TAG"

# Catalog and authoring templates belong to the controller. They never modify
# the immutable signed archive/tree served to a Guard.
RUN set -eu; \
    base="https://github.com/${STACKKIT_RELEASE_REPO}/releases/download/${STACKKIT_RELEASE_TAG}"; \
    curl --fail --silent --show-error --location "${base}/stackkits-release-index-v1.json" --output /tmp/index.json; \
    echo "${STACKKIT_RELEASE_INDEX_SHA256}  /tmp/index.json" | sha256sum -c -; \
    mkdir -p /app/stackkits; \
    for kit in basement-kit cloud-kit modern-homelab; do \
      entry="$(jq -er --arg kit "$kit" --arg version "$STACKKIT_RELEASE_TAG" \
        '[.assets[] | select(.kit == $kit and .version == $version and .platform.os == "linux" and .platform.arch == "amd64") | .archive] | if length == 1 then .[0].name + " " + .[0].sha256 else error("ambiguous or missing archive") end' /tmp/index.json)"; \
      set -- $entry; \
      curl --fail --silent --show-error --location "${base}/$1" --output /tmp/catalog.tar.gz; \
      echo "$2  /tmp/catalog.tar.gz" | sha256sum -c -; \
      tar --extract --gzip --file /tmp/catalog.tar.gz --directory /app/stackkits \
        --exclude=stackkit --exclude=stackkit-server --exclude=stackkit-mcp --exclude=stackkit-content-bridge \
        --exclude=tofu --exclude=terramate --exclude=providers; \
      test -s "/app/stackkits/$kit/stackkit.yaml"; \
    done; \
    bash scripts/new-linux-stackkit-spec-templates.sh; \
    cp -R dist/spec-templates /app/stackkits/spec-templates

FROM alpine:3.24
ARG GIT_COMMIT=""
ARG TECHSTACK_PRODUCT_VERSION=""
LABEL org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.version="${TECHSTACK_PRODUCT_VERSION}" \
      org.opencontainers.image.source="https://github.com/kombifyio/TechStack"
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates curl docker-cli docker-cli-compose git jq openssh-client
RUN addgroup -S techstack && adduser -S -G techstack techstack && mkdir -p /data && chown techstack:techstack /data
COPY --from=backend /out/techstack /app/techstack
COPY --from=signed-runtime /app/.stackkit /app/.stackkit
COPY --from=signed-runtime /app/stackkits /app/stackkits
COPY --from=signed-runtime /app/guard-release /app/guard-release
ENV KOMBIFY_EDITION=selfhost-oss
ENV TECHSTACK_AUTH_MODE=local
ENV TECHSTACK_DATA_DIR=/data
ENV TECHSTACK_STACKKITS_DIR=/app/stackkits
ENV TECHSTACK_STACKKIT_CLI=/app/.stackkit/bin/stackkit
ENV TECHSTACK_STACKKIT_ARTIFACTS_MODE=cli
ENV TECHSTACK_STACKKIT_RELEASE_CACHE=/app/.stackkit/releases
ENV TECHSTACK_STACKKIT_RELEASE_PIN=/app/.stackkit/stackkits-release-pin.json
ENV TECHSTACK_STACKKIT_COMPATIBILITY_MANIFEST=/app/.stackkit/stackkits-compatibility-v1.json
ENV TECHSTACK_STACKKIT_USE_CASE_CATALOG=/app/.stackkit/stackkits-use-case-catalog-v1.json
ENV TECHSTACK_STACKKIT_SPEC_TEMPLATES=/app/stackkits/spec-templates
ENV TECHSTACK_STACKKIT_RELEASE_BUNDLE=/app/guard-release/stackkit-runtime-linux-amd64.tar.gz
ENV TECHSTACK_GUARD_RELEASE_DIR=/app/guard-release
ENV TECHSTACK_GUARD_RELEASE_SOURCE=kombifyio/TechStack
ENV PORT=5260
ENV TECHSTACK_PORT=5260
EXPOSE 5260 5263
WORKDIR /app
VOLUME ["/data"]
USER techstack
ENTRYPOINT ["/app/techstack"]
