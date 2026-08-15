// Package v2 is the transitional typed service surface inside TechStack.
//
// It runs alongside the older PocketBase-backed runtime while the lean-core
// rebuild moves selected flows onto newer services. The PocketBase path remains
// authoritative until each module is cut over and gated by
// TECHSTACK_LEGACY_PB_ENABLED.
//
// V2 is intentionally minimal at Phase 0. Phase 1 wires pgx/SQLC, Phase 2 wires
// OIDC. Do not import this package from production runtime entry points until
// Phase 1 has landed.
package v2

// Version identifies the V2 surface. It is bumped by hand as the V2 API
// stabilises and is exposed via the /api/v2/health endpoint.
const Version = "v2.0.0-alpha.0"
