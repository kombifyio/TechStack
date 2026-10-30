// Package featureflags provides a client and middleware for consuming
// feature flags from the kombify runtime projection. Server runtimes should
// configure DatabaseURL so reads go directly to kombify-DB. The Administration
// HTTP endpoint remains a compatibility fallback during migration.
//
// If a Go-side evaluator is ever needed again, add it with a conformance
// test harness that shares fixtures with the Administration TS evaluator
// so the two cannot drift silently.
package featureflags

// AdminFeatureFlag is the wire format returned by the Admin API
// compat endpoint (GET /feature-flags). Already pre-evaluated.
type AdminFeatureFlag struct {
	Key         string         `json:"key"`
	Enabled     bool           `json:"enabled"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// AdminFlagsResponse is the response from GET /feature-flags.
type AdminFlagsResponse struct {
	Success bool                        `json:"success"`
	Data    map[string]AdminFeatureFlag `json:"data"`
}
