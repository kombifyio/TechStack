// Package local is a thin compatibility shim that re-exports the shared
// [authlocal] package from kombify-go-common.
//
// Migration history: this package was the original donor of go-common's
// authlocal (lifted 2026-05-03 as part of the kombify auth standardization).
// Since 2026-05-04 (Beads kombifyStack-v2a.2) it carries no divergent logic
// and only re-exports the upstream types so existing TechStack import paths
// (`pkg/v2/auth/local`) keep working without behavioral change.
//
// The TechStack-specific break-glass email default (`breakglass@techstack.local`)
// is preserved here as [BreakGlassEmail] so callers that don't pass an
// explicit `BootstrapEmail` keep the historic value.
//
// New code SHOULD import
// `github.com/kombifyio/go-common/authlocal` directly. This
// shim will be removed once the last consumer (`internal/breakglass/pocketbase.go`)
// is migrated; track the cleanup in Beads kombifyStack-v2a.3 / v2a.4.
package local

import (
	"github.com/kombifyio/go-common/authlocal"
)

// BreakGlassEmail preserves the historic TechStack break-glass admin email
// (`breakglass@techstack.local`). go-common defaults to `breakglass@local`;
// production wiring in cmd/techstack/main.go passes this constant explicitly
// via Config.BootstrapEmail.
const BreakGlassEmail = "breakglass@techstack.local"

// Re-exported constants from go-common/authlocal.
const (
	BreakGlassRecordID  = authlocal.BreakGlassRecordID
	PasswordEnvelopeTTL = authlocal.PasswordEnvelopeTTL
	DefaultProviderID   = authlocal.DefaultProviderID
)

// Re-exported error sentinels from go-common/authlocal.
var (
	ErrInvalidConfig    = authlocal.ErrInvalidConfig
	ErrInvalidCreds     = authlocal.ErrInvalidCreds
	ErrAlreadyClaimed   = authlocal.ErrAlreadyClaimed
	ErrNotFound         = authlocal.ErrNotFound
	ErrPasswordExpired  = authlocal.ErrPasswordExpired
	ErrLockedByOperator = authlocal.ErrLockedByOperator
)

// Re-exported types from go-common/authlocal. These are type aliases so a
// `*local.Record` and a `*authlocal.Record` are the exact same value.
type (
	Record       = authlocal.Record
	Store        = authlocal.Store
	Service      = authlocal.Service
	Config       = authlocal.Config
	ClaimRequest = authlocal.ClaimRequest
	RevealResult = authlocal.RevealResult
	Status       = authlocal.Status
	Handlers     = authlocal.Handlers
)

// New is a re-export of [authlocal.New].
func New(cfg Config) (*Service, error) {
	if cfg.BootstrapEmail == "" {
		cfg.BootstrapEmail = BreakGlassEmail
	}
	return authlocal.New(cfg)
}
