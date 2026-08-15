// Package session is a thin compatibility shim that re-exports the shared
// [authsession] package from kombify-go-common.
//
// Migration history: this package was the original donor of authsession
// (lifted 2026-05-03 as part of the kombify auth standardization). It now
// re-exports the upstream types so existing TechStack import paths
// (`pkg/v2/auth/session`) keep working without behavioral change.
//
// New code SHOULD import `github.com/kombifyio/go-common/authsession`
// directly. This shim will be removed once all call sites have been
// migrated; track the cleanup in Beads.
package session

import (
	"strings"

	"github.com/kombifyio/go-common/authsession"
)

// DefaultIssuer preserves the historic TechStack issuer value ("techstack")
// for backwards compatibility. Tokens minted via NewManager without an
// explicit Issuer continue to carry iss=techstack so existing verifiers
// that pin the iss claim keep working.
const DefaultIssuer = "techstack"

// DefaultLifetime mirrors authsession.DefaultLifetime.
const DefaultLifetime = authsession.DefaultLifetime

// Re-exported error sentinels.
var (
	ErrConfigInvalid = authsession.ErrConfigInvalid
	ErrInvalidToken  = authsession.ErrInvalidToken
)

// Type aliases keep struct field access identical to the donor API.
type (
	Config  = authsession.Config
	Claims  = authsession.Claims
	Manager = authsession.Manager
)

// NewManager validates Config and returns a Manager. When Config.Issuer is
// empty we substitute the legacy [DefaultIssuer] ("techstack") instead of
// authsession's "kombify" default, preserving wire compatibility for the
// V2 backend.
func NewManager(cfg Config) (*Manager, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		cfg.Issuer = DefaultIssuer
	}
	return authsession.NewManager(cfg)
}
