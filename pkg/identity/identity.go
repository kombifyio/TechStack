// Package identity re-exports the canonical identity types from kombify-go-common.
// All downstream code continues to import "github.com/kombifyio/techstack/pkg/identity"
// unchanged, but the implementation lives in the shared module.
package identity

import (
	common "github.com/kombifyio/go-common/identity"
)

// Type aliases so existing imports continue to work.
type Identity = common.Identity

// Function re-exports.
var (
	NewContext  = common.NewContext
	FromContext = common.FromContext
	FromHeaders = common.FromHeaders
)
