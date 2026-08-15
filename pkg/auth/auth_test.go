package auth

import (
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	password := "securePassword123!"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	// Should start with argon2id marker
	if hash[:9] != "$argon2id" {
		t.Errorf("Hash should start with $argon2id, got: %s", hash[:20])
	}

	// Verify correct password
	valid, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !valid {
		t.Error("Password should be valid")
	}

	// Verify wrong password
	valid, err = VerifyPassword("wrongPassword", hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed for wrong password: %v", err)
	}
	if valid {
		t.Error("Wrong password should not be valid")
	}
}

func TestAPIKeyGeneration(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	// Key should start with ks_ prefix
	if key.Key[:3] != "ks_" {
		t.Errorf("Key should start with 'ks_', got: %s", key.Key[:10])
	}

	// Prefix should be extractable
	if key.Prefix != key.Key[:11] {
		t.Errorf("Prefix mismatch: expected %s, got %s", key.Key[:11], key.Prefix)
	}

	// Verify key against hash
	if !VerifyAPIKey(key.Key, key.Hash) {
		t.Error("API key verification failed")
	}

	// Wrong key should fail
	if VerifyAPIKey("wrong_key", key.Hash) {
		t.Error("Wrong key should not verify")
	}
}

func TestGetAPIKeyPrefix(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	extracted := GetAPIKeyPrefix(key.Key)
	if extracted != key.Prefix {
		t.Errorf("GetAPIKeyPrefix failed: expected %s, got %s", key.Prefix, extracted)
	}

	// Too short key should return empty
	if GetAPIKeyPrefix("short") != "" {
		t.Error("Short key should return empty prefix")
	}
}

func TestSessionToken(t *testing.T) {
	token, hash, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken failed: %v", err)
	}

	// Token should be base64
	if len(token) < 20 {
		t.Error("Token should be reasonably long")
	}

	// Hash should match when computed again
	computedHash := HashSessionToken(token)
	if computedHash != hash {
		t.Error("Session token hash mismatch")
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = HashPassword("benchmarkPassword123")
	}
}

func BenchmarkVerifyPassword(b *testing.B) {
	hash, err := HashPassword("benchmarkPassword123")
	if err != nil {
		b.Fatalf("HashPassword failed: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = VerifyPassword("benchmarkPassword123", hash)
	}
}
