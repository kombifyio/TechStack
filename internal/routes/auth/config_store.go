package auth

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
)

// LocalOwnerLookup is the canonical local owner store (authlocal / breakglass).
// First-run and setup availability consult this store, not PocketBase auth_config.
type LocalOwnerLookup interface {
	Get(ctx context.Context) (*authlocal.Record, error)
}

func canonicalOwnerPresent(ctx context.Context, store LocalOwnerLookup) (bool, error) {
	if store == nil {
		return false, nil
	}
	record, err := store.Get(ctx)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, nil
	}
	// Bootstrap creates an unclaimed break-glass row before these routes are
	// served. Only the canonical claim completes first-owner setup.
	return record.Claimed && strings.TrimSpace(record.Email) != "", nil
}
