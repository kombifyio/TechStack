// Package openapi embeds the kombifyTechstack OpenAPI specification.
package openapi

import _ "embed"

//go:embed techstack-v1.yaml
var Spec []byte

// InternalSpec is deliberately separate from Spec so public API discovery and
// generated clients never expose servicecall-only RuntimeLease operations.
//
//go:embed techstack-internal-v1.yaml
var InternalSpec []byte
