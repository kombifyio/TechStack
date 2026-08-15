# kombify-TechStack App

SvelteKit operator UI for the TechStack control plane.

## Scope

The app owns:

- dashboard and control-plane UI
- worker, stack, monitoring, and setup flows
- Playwright and Vitest frontend tests

The app does not own Kombify Cloud account/commerce workflows, provider driver
execution, or agent reasoning surfaces.

## Commands

From the repo root, prefer:

```bash
mise run dev:app
mise run test:unit
mise run test:e2e
mise run lint
```

From this directory:

```bash
pnpm install
pnpm dev
pnpm build
pnpm check
pnpm lint
pnpm test:unit
```

## Runtime

The app builds as a static SPA (`adapter-static`, output `build-static/`) and
is embedded into the Go binary (`internal/frontend/dist`,
`-tags techstack_static_ui`), which serves it in every deployment (ADR-033
OQ2). There is no Node SSR process.

- local dev server (vite): `http://localhost:5261`
- dev backend proxy target: `http://localhost:5260` (vite `server.proxy`,
  override with `TECHSTACK_DEV_API_PROXY`)
- production origin: `https://techstack.kombify.io`

Frontend build-time variables use `VITE_*`. Runtime-varying public config
(telemetry, edition) is fetched once at startup from
`GET /api/v1/client/bootstrap` (`src/lib/client/bootstrap.ts`).
