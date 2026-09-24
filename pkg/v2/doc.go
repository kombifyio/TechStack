// Package v2 contains Techstack's active browser session, identity, and
// /api/v2 health surfaces. cmd/techstack wires it on every production start
// over the Postgres control-plane authority.
//
// The v2 package name is transitional and must not grow unrelated product
// authority. New domain surfaces belong in named domain packages; the bounded
// PocketBase bootstrap that remains elsewhere is auth compatibility only.
package v2

// Version identifies the V2 surface. It is bumped by hand as the V2 API
// stabilises and is exposed via the /api/v2/health endpoint.
const Version = "v2.0.0-alpha.0"
