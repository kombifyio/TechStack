package auth

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/pocketbase_migration"
	"github.com/pocketbase/pocketbase/core"
)

// LocalOwnerLookup is the canonical local owner store (authlocal / breakglass).
// First-run and setup availability consult this store, not PocketBase auth_config.
type LocalOwnerLookup interface {
	Get(ctx context.Context) (*authlocal.Record, error)
}

// loadAuthConfig is the compatibility reader for mode/allow_local_login.
// First-run must not use this marker when LocalOwnerLookup is available.
func loadAuthConfig(app core.App) (*core.Record, error) {
	return pocketbase_migration.LoadAuthConfig(app)
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
	// Local auth bootstrap inserts the owner row before /auth/mode is served.
	// An email-bearing record means setup already has an authority; first-run
	// create must not run again.
	return strings.TrimSpace(record.Email) != "", nil
}
