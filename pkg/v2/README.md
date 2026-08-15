# pkg/v2 — Transitional TechStack API Surface

This package is the typed transitional service / API surface used while
TechStack moves away from the older PocketBase-centred runtime.
"`v2`" is **not** a product label — it names the migration layer only.
The product narrative lives under the three pillars (Unifier,
Monitoring, RIL); see [`../../docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md).

The transitional naming is scheduled to be retired in master
sanitization phase 6 (begriffs-/naming-hygiene) — the auth submodules
(`pkg/v2/auth`, `pkg/v2/authmw`) stay during the migration; the rest is
rename-or-integrate.

## Status — Wired Core

- `pkg/v2` is wired from `cmd/techstack/main.go` behind
  `TECHSTACK_V2_ENABLED`.
- `/api/v2/health` reports the wired DB backend status.
- `/api/v2/whoami` is available when a session manager is attached.
- `/api/v2/auth/*` is available when auth providers are configured.
- The frontend-facing local auth compatibility surface is also served
  via `/api/v1/auth/methods`, `/api/v1/auth/login`, `/api/v1/auth/logout`,
  and the breakglass endpoints when those handlers are attached.

## What goes here

The transitional surface owns:

- `/api/v2/*` HTTP surface
- pgx + SQLC repositories (`pkg/db/`)
- OIDC verifier (`pkg/auth/oidc/`)
- Tenant middleware (RLS-aware)
- Newer service packages (stacks, jobs, workers, drift, prechecks,
  wallet, …)

The transitional surface does **not** own:

- PocketBase migrations (frozen, see
  `.github/workflows/guard-pb-migrations-frozen.yml`)
- User / Org / Subscription / Billing (central Prisma DB repo + Admin API)
- Feature flags / tier (Admin API via `go-common/featureflags`)
- Product-scope decisions; those live in the pillar docs and the active
  sub-plans under [`../../docs/plans/`](../../docs/plans/)
- VM-lease authority logic; that lives in
  [`../vmleases/`](../vmleases/) with its contract in
  `kombify-runtime-contracts-go/runtimelease` at pinned release `v0.1.4`

## Running locally

The transitional surface is registered from `cmd/techstack/main.go` by
default. Disable it explicitly with `TECHSTACK_V2_ENABLED=false` if you
need to fall back to the legacy-only runtime. Unit tests cover the
package directly:

```
go test ./pkg/v2/...
```

Auth / session wiring is exercised from the app layer through the
focused auth tests and the no-setup Playwright flow.

## Wiring plan

Next phases keep moving more domain surfaces (stacks, jobs, workers,
drift, prechecks, wallet, and related repositories) onto the
Postgres-backed path while keeping PocketBase migrations frozen except
for explicitly allowed bridge changes. Once those moves complete, this
package's `v2` naming gets retired in favour of clean per-domain
packages — only `pkg/v2/auth` and `pkg/v2/authmw` are expected to keep
the `v2` prefix during the auth-migration window.
