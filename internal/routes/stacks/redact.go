package stacks

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

const recoverySensitiveKey = "recovery"

// redactUserConfigForStorage deep-copies the provided config and removes fields
// that look like secrets. This is used for persisting user_config in the DB.
//
// IMPORTANT:
// - The Unifier/orchestrator may still need the original config to run once.
// - Persisting plaintext secrets in the stack record is unsafe.
func redactUserConfigForStorage(in map[string]interface{}) map[string]interface{} {
	outAny := cloneAndRedactAny(in)
	out, ok := outAny.(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	return out
}

func redactUserConfigRawForStorage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if parsed, ok := parseRawUserConfigMap(raw); ok {
		redacted := redactUserConfigForStorage(parsed)
		if data, marshalErr := json.Marshal(redacted); marshalErr == nil {
			return string(data)
		}
	}

	if rawContainsSensitiveMarker(raw) {
		return ""
	}
	return raw
}

func containsPlaintextRecoveryMaterialInRequest(req normalizedCreateStackRequest) bool {
	if containsPlaintextRecoveryMaterial(req.UserConfig, false) {
		return true
	}
	if containsPlaintextRecoveryMaterial(req.Options, false) {
		return true
	}
	raw := strings.TrimSpace(req.UserConfigRaw)
	if raw == "" {
		return false
	}
	if parsed, ok := parseRawUserConfigMap(raw); ok {
		return containsPlaintextRecoveryMaterial(parsed, false)
	}
	return rawContainsPlaintextRecoveryMarker(raw)
}

func parseRawUserConfigMap(raw string) (map[string]interface{}, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
		return parsed, true
	}
	parsed = nil
	if err := yaml.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
		return parsed, true
	}
	return nil, false
}

func containsPlaintextRecoveryMaterial(value any, inRecoveryContext bool) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		return containsPlaintextRecoveryMap(typed, inRecoveryContext)
	case map[string]string:
		return containsPlaintextRecoveryStringMap(typed, inRecoveryContext)
	case []interface{}:
		for _, item := range typed {
			if containsPlaintextRecoveryMaterial(item, inRecoveryContext) {
				return true
			}
		}
	}
	return false
}

func containsPlaintextRecoveryMap(value map[string]interface{}, inRecoveryContext bool) bool {
	for key, item := range value {
		normalizedKey := normalizeSensitiveKey(key)
		nextRecoveryContext := nextPlaintextRecoveryContext(normalizedKey, inRecoveryContext)
		if isPlaintextRecoveryKey(normalizedKey, nextRecoveryContext) && hasPlaintextValue(item) {
			return true
		}
		if containsPlaintextRecoveryMaterial(item, nextRecoveryContext) {
			return true
		}
	}
	return false
}

func containsPlaintextRecoveryStringMap(value map[string]string, inRecoveryContext bool) bool {
	for key, item := range value {
		normalizedKey := normalizeSensitiveKey(key)
		nextRecoveryContext := nextPlaintextRecoveryContext(normalizedKey, inRecoveryContext)
		if isPlaintextRecoveryKey(normalizedKey, nextRecoveryContext) && strings.TrimSpace(item) != "" {
			return true
		}
	}
	return false
}

func nextPlaintextRecoveryContext(normalizedKey string, current bool) bool {
	return current || normalizedKey == recoverySensitiveKey || strings.Contains(normalizedKey, recoverySensitiveKey)
}

func isPlaintextRecoveryKey(normalizedKey string, inRecoveryContext bool) bool {
	if normalizedKey == "" {
		return false
	}
	if strings.Contains(normalizedKey, "hash") ||
		strings.Contains(normalizedKey, "ref") ||
		strings.Contains(normalizedKey, "reference") ||
		strings.Contains(normalizedKey, "present") {
		return false
	}
	if strings.Contains(normalizedKey, recoverySensitiveKey) && strings.Contains(normalizedKey, "passphrase") {
		return true
	}
	return inRecoveryContext && (normalizedKey == "passphrase" || normalizedKey == "plaintext")
}

func hasPlaintextValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case nil:
		return false
	default:
		return true
	}
}

func normalizeSensitiveKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	return replacer.Replace(key)
}

func cloneAndRedactAny(v any) any {
	switch typed := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, val := range typed {
			if isSensitiveKey(k) {
				// Drop the key entirely for storage.
				continue
			}
			out[k] = cloneAndRedactAny(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneAndRedactAny(item))
		}
		return out
	default:
		return v
	}
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}

	// Explicit high-risk keys.
	switch k {
	case "password", "passwordconfirm", "passphrase":
		return true
	case "token", "registrationtoken", "accesstoken", "refreshtoken":
		return true
	case "secret", "clientsecret", "apisecret":
		return true
	case "apikey", "api_key", "accesskey", "access_key":
		return true
	case "privatekey", "private_key":
		return true
	}

	// Heuristic: common secret substrings.
	if strings.Contains(k, "password") || strings.Contains(k, "secret") {
		return true
	}
	if strings.Contains(k, "passphrase") {
		return true
	}
	if strings.Contains(k, "token") || strings.Contains(k, "private") {
		return true
	}

	return false
}

func rawContainsSensitiveMarker(raw string) bool {
	lower := strings.ToLower(raw)
	for _, marker := range []string{
		"password",
		"passphrase",
		"secret",
		"token",
		"private_key",
		"apikey",
		"api_key",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func rawContainsPlaintextRecoveryMarker(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, recoverySensitiveKey) &&
		strings.Contains(lower, "passphrase")
}
