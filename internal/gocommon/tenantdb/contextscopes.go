package tenantdb

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

// ContextScopeSchemaSQL adds capability accessors without changing the existing
// verified_context identity ABI. Apply after ContextSchemaSQL and, when used,
// LocalContextSchemaSQL. Consumers may narrow the default accessor EXECUTE ACLs.
//
//go:embed sql/kombify_ctx_scopes_v1.sql
var ContextScopeSchemaSQL string

var contextScopePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_./-]{0,127}$`)

func validContextScopes(scopes []string) bool {
	if len(scopes) > 128 {
		return false
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if !contextScopePattern.MatchString(scope) || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}

// Invalid or absent scope metadata never becomes an implicit capability. The
// signed identity remains compatible with pre-scope consumers.
func decodeContextScopes(raw json.RawMessage) []string {
	var scopes []string
	if len(raw) == 0 || json.Unmarshal(raw, &scopes) != nil || !validContextScopes(scopes) {
		return nil
	}
	return scopes
}

// ContextScopesPgx reads only database-verified capabilities. Call after
// ApplyContextTokenPgx and its identity comparison in the same transaction.
// An empty result grants nothing; SQL policies must enforce scopes themselves.
func ContextScopesPgx(ctx context.Context, tx PgxTxQuerier) ([]string, error) {
	var scopes []string
	if err := tx.QueryRow(ctx, `SELECT kombify_ctx.scopes()`).Scan(&scopes); err != nil {
		return nil, fmt.Errorf("tenantdb: verify context scopes: %w", err)
	}
	return scopes, nil
}
