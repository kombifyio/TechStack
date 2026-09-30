// Package surface embeds the generated agent-native API surface of the
// Techstack contract (API-FIRST-STANDARD section 9). api-surface.json is
// generated from api/openapi/techstack-v1.yaml by `mise run api:surface`;
// never edit it by hand.
package surface

import _ "embed"

// Raw is the generated kombify.api-surface/v1 document.
//
//go:embed api-surface.json
var Raw []byte
