# syntax=docker/dockerfile:1.7

FROM node:24-slim AS frontend
WORKDIR /build/app
RUN corepack enable && corepack prepare pnpm@11.5.2 --activate
COPY app/package.json app/pnpm-lock.yaml app/pnpm-workspace.yaml app/.npmrc ./
COPY app/scripts/install-deps.mjs ./scripts/install-deps.mjs
RUN pnpm install --frozen-lockfile
COPY VERSION /build/VERSION
COPY app/ .
ENV PUBLIC_KOMBIFY_EDITION=selfhost-oss
RUN pnpm build

FROM golang:1.26.5-alpine AS backend
WORKDIR /build
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /build/app/build-static ./internal/frontend/dist
ARG TECHSTACK_PRODUCT_VERSION=""
ARG GIT_COMMIT=""
RUN set -eu; \
    version="${TECHSTACK_PRODUCT_VERSION:-$(cat VERSION)}"; \
    printf '%s' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; \
    revision="${GIT_COMMIT:-dev}"; \
    if [ "$revision" != dev ]; then printf '%s' "$revision" | grep -Eq '^[0-9a-f]{40}$'; fi; \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath \
      -tags techstack_static_ui \
      -ldflags="-s -w -X main.version=${version} -X main.buildRevision=${revision}" \
      -o /out/techstack ./cmd/techstack

FROM alpine:3.22
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates curl docker-cli docker-cli-compose git jq openssh-client
RUN addgroup -S techstack && adduser -S -G techstack techstack && mkdir -p /data && chown techstack:techstack /data
COPY --from=backend /out/techstack /app/techstack
ENV KOMBIFY_EDITION=selfhost-oss
ENV TECHSTACK_AUTH_MODE=local
ENV TECHSTACK_DATA_DIR=/data
ENV PORT=5260
ENV TECHSTACK_PORT=5260
EXPOSE 5260 5263
VOLUME ["/data"]
USER techstack
ENTRYPOINT ["/app/techstack"]
