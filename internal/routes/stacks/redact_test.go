package stacks

import "testing"

func TestRedactUserConfigForStorage_RemovesSecretsAndDoesNotMutateOriginal(t *testing.T) {
	original := map[string]interface{}{
		"name": "techstack",
		"admin": map[string]interface{}{
			"username": "admin",
			"email":    "admin@example.com",
			"password": "super-secret",
		},
		"auth": map[string]interface{}{
			"requirePassword": true,
		},
		"nested": map[string]interface{}{
			"apiKey": "should-not-persist",
			"ok":     "keep",
		},
	}

	redacted := redactUserConfigForStorage(original)

	// Original still contains secrets (input must not be mutated).
	admin := original["admin"].(map[string]interface{})
	if admin["password"] != "super-secret" {
		t.Fatalf("expected original admin.password to remain")
	}

	// Redacted copy should keep non-sensitive fields.
	redAdminAny, ok := redacted["admin"]
	if !ok {
		t.Fatalf("expected redacted to keep admin object")
	}
	redAdmin := redAdminAny.(map[string]interface{})
	if redAdmin["username"] != "admin" {
		t.Fatalf("expected username to be preserved")
	}
	if _, has := redAdmin["password"]; has {
		t.Fatalf("expected password to be removed")
	}

	nested := redacted["nested"].(map[string]interface{})
	if _, has := nested["apiKey"]; has {
		t.Fatalf("expected apiKey to be removed")
	}
	if nested["ok"] != "keep" {
		t.Fatalf("expected nested.ok to be preserved")
	}
}
