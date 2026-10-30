package tenantdb

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

// ContextCapabilitySchemaSQL adds capability accessors without changing the existing
// verified_context identity ABI. Apply after ContextSchemaSQL. Local proof
// grants no signed product capabilities; use its separately verified scopes. Consumers may narrow the default accessor EXECUTE ACLs.
//
//go:embed sql/kombify_ctx_capabilities_v1.sql
var ContextCapabilitySchemaSQL string

var contextCapabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)+$`)

func validContextCapabilities(capabilities []string) bool {
	if len(capabilities) > 128 {
		return false
	}
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if len(capability) > 128 || !contextCapabilityPattern.MatchString(capability) || seen[capability] {
			return false
		}
		seen[capability] = true
	}
	return true
}

// Invalid or absent capability metadata never becomes an implicit capability. The
// signed identity remains compatible with pre-capability consumers.
func decodeContextCapabilities(raw json.RawMessage) []string {
	var capabilities []string
	if len(raw) == 0 || json.Unmarshal(raw, &capabilities) != nil || !validContextCapabilities(capabilities) {
		return nil
	}
	return capabilities
}

// ContextCapabilitiesPgx reads only database-verified capabilities. Call after
// ApplyContextTokenPgx and its identity comparison in the same transaction.
// An empty result grants nothing; SQL policies must enforce capabilities themselves.
func ContextCapabilitiesPgx(ctx context.Context, tx PgxTxQuerier) ([]string, error) {
	var capabilities []string
	if err := tx.QueryRow(ctx, `SELECT kombify_ctx.capabilities()`).Scan(&capabilities); err != nil {
		return nil, fmt.Errorf("tenantdb: verify context capabilities: %w", err)
	}
	return capabilities, nil
}
