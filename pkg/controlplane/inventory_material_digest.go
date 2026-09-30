package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// inventoryMaterialDigestKey is the server metadata key holding the digest of
// the inventory behind the current inventory revision.
const inventoryMaterialDigestKey = "inventory_material_digest"

// inventoryFreshnessKeys change with every observation without changing what
// runs on the node, so they never move the inventory revision.
var inventoryFreshnessKeys = map[string]bool{
	"cpu_percent": true, "memory_used_bytes": true, "disk_used_bytes": true, "uptime_seconds": true,
	// The runtime convergence snapshot restamps its components on every
	// observation; the Guard projection envelope carries the source position.
	"runtime_convergence": true, guardProjectionEnvelopeKey: true,
	"timestamp": true, "heartbeat": true,
}

// inventoryMaterialDigest is the SHA-256 of the inventory without freshness
// fields: every key in inventoryFreshnessKeys and every key ending in "_at"
// is dropped at any depth. encoding/json sorts map keys, so the encoding is
// canonical. "" when the inventory cannot be encoded.
func inventoryMaterialDigest(inventory map[string]any) string {
	encoded, err := json.Marshal(inventoryMaterialValue(inventory))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func inventoryMaterialValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if inventoryFreshnessKeys[normalized] || strings.HasSuffix(normalized, "_at") {
				continue
			}
			out[key] = inventoryMaterialValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = inventoryMaterialValue(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = inventoryMaterialValue(item)
		}
		return out
	default:
		// Typed values (structs, typed slices) go through JSON once so their
		// nested maps are filtered too.
		encoded, err := json.Marshal(typed)
		if err != nil {
			return typed
		}
		var generic any
		if json.Unmarshal(encoded, &generic) != nil {
			return typed
		}
		switch generic.(type) {
		case map[string]any, []any:
			return inventoryMaterialValue(generic)
		default:
			return generic
		}
	}
}

func stringFromMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
