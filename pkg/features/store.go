package features

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a record is not found (not an error condition for optional lookups)
var ErrNotFound = errors.New("record not found")

// Store defines the read interface for feature flag persistence.
// Flag management is central (Cloudflare Edge/OpenFeature entitlements); local stores only serve
// previously recorded per-user preferences and consents.
type Store interface {
	// User flag preferences
	GetUserFlag(ctx context.Context, userID, featureKey string) (*bool, error)

	// Batch operations (for N+1 query optimization)
	GetUserFlags(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error)
	GetUserConsentsMap(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error)

	// User consent tracking
	HasUserConsent(ctx context.Context, userID, featureKey string) (bool, error)
}
