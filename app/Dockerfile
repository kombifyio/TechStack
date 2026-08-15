# syntax=docker/dockerfile:1.7
# Standalone frontend image for docker-compose dev/e2e flows: serves the
# adapter-static SPA build with a small no-dependency static server that
# proxies backend paths to TECHSTACK_API_URL (ADR-033 OQ2 — no adapter-node
# SSR runtime). Production does not use this image; the Go binary embeds and
# serves the SPA itself.

FROM node:24-slim AS builder
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
RUN corepack enable && corepack prepare pnpm@11.5.2 --activate
WORKDIR /build/app
COPY app/package.json app/pnpm-lock.yaml app/pnpm-workspace.yaml app/.npmrc ./
COPY app/scripts/install-deps.mjs ./scripts/install-deps.mjs
RUN --mount=type=cache,id=techstack-app-pnpm-store,target=/pnpm/store \
    set -eu; \
    pnpm config set store-dir /pnpm/store; \
    node scripts/install-deps.mjs --frozen-lockfile
COPY VERSION /build/VERSION
COPY app/ .
ARG VITE_POCKETBASE_URL=""
ARG VITE_API_URL=""
ARG VITE_KOMBISIM_URL=""
RUN pnpm build

FROM node:24-alpine
WORKDIR /app
COPY --from=builder --chown=node:node /build/app/build-static /app/build-static
COPY --from=builder --chown=node:node /build/app/scripts/serve-static.mjs /app/scripts/serve-static.mjs
USER node
EXPOSE 5261
ENV PORT=5261
ENV HOST=0.0.0.0
ENV TECHSTACK_STATIC_DIR=/app/build-static
CMD ["node", "scripts/serve-static.mjs"]
