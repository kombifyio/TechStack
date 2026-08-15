package auth

import (
	"strings"
	"testing"
)

func TestSecretEncryptor_EncryptDecrypt(t *testing.T) {
	// Generate a 32-byte key (AES-256)
	key := []byte("test-key-must-be-32-bytes-long!!")

	enc, err := NewSecretEncryptor(key)
	if err != nil {
		t.Fatalf("NewSecretEncryptor failed: %v", err)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{"empty", ""},
		{"simple", "hello world"},
		{"password", "super-secret-password-123!@#"},
		{"unicode", "日本語テスト🎉"},
		{"long", strings.Repeat("a", 10000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.plaintext == "" {
				// Empty string handled specially
				result, err := EncryptIfNeeded(enc, "")
				if err != nil || result != "" {
					t.Errorf("Empty string should return empty")
				}
				return
			}

			encrypted, err := enc.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}

			// Verify prefix
			if !strings.HasPrefix(encrypted, EncryptedPrefix) {
				t.Errorf("Encrypted value should have prefix %q", EncryptedPrefix)
			}

			// Verify IsEncrypted
			if !IsEncrypted(encrypted) {
				t.Error("IsEncrypted should return true for encrypted value")
			}

			// Decrypt
			decrypted, err := enc.Decrypt(encrypted)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}

			if decrypted != tt.plaintext {
				t.Errorf("Decrypted value = %q, want %q", decrypted, tt.plaintext)
			}
		})
	}
}

func TestSecretEncryptor_KeyValidation(t *testing.T) {
	// Key too short
	_, err := NewSecretEncryptor([]byte("short"))
	if err != ErrKeyTooShort {
		t.Errorf("Expected ErrKeyTooShort for short key, got %v", err)
	}

	// Key too long
	_, err = NewSecretEncryptor([]byte(strings.Repeat("a", 64)))
	if err != ErrKeyTooShort {
		t.Errorf("Expected ErrKeyTooShort for long key, got %v", err)
	}

	// Exactly 32 bytes should work
	_, err = NewSecretEncryptor([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Errorf("32-byte key should be valid, got error: %v", err)
	}
}

func TestSecretEncryptor_InvalidCiphertext(t *testing.T) {
	key := []byte("test-key-must-be-32-bytes-long!!")
	enc, _ := NewSecretEncryptor(key)

	tests := []struct {
		name       string
		ciphertext string
	}{
		{"no_prefix", "not-encrypted-value"},
		{"wrong_prefix", "enc:v2:invalid"},
		{"invalid_base64", EncryptedPrefix + "not-valid-base64!!!"},
		{"truncated", EncryptedPrefix + "dG9v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := enc.Decrypt(tt.ciphertext)
			if err == nil {
				t.Error("Expected error for invalid ciphertext")
			}
		})
	}
}

func TestEncryptIfNeeded(t *testing.T) {
	key := []byte("test-key-must-be-32-bytes-long!!")
	enc, _ := NewSecretEncryptor(key)

	// Encrypt plaintext
	result, err := EncryptIfNeeded(enc, "secret")
	if err != nil {
		t.Fatalf("EncryptIfNeeded failed: %v", err)
	}
	if !IsEncrypted(result) {
		t.Error("Result should be encrypted")
	}

	// Should not double-encrypt
	result2, err := EncryptIfNeeded(enc, result)
	if err != nil {
		t.Fatalf("EncryptIfNeeded (second call) failed: %v", err)
	}
	if result != result2 {
		t.Error("Already encrypted value should not be re-encrypted")
	}

	// Nil encryptor should return value as-is
	result3, err := EncryptIfNeeded(nil, "plaintext")
	if err != nil {
		t.Fatalf("EncryptIfNeeded with nil encryptor failed: %v", err)
	}
	if result3 != "plaintext" {
		t.Error("Nil encryptor should return value unchanged")
	}
}

func TestDecryptIfNeeded(t *testing.T) {
	key := []byte("test-key-must-be-32-bytes-long!!")
	enc, _ := NewSecretEncryptor(key)

	// Encrypt then decrypt
	encrypted, _ := enc.Encrypt("secret")
	result, err := DecryptIfNeeded(enc, encrypted)
	if err != nil {
		t.Fatalf("DecryptIfNeeded failed: %v", err)
	}
	if result != "secret" {
		t.Errorf("Decrypted value = %q, want %q", result, "secret")
	}

	// Non-encrypted value should be returned as-is
	result2, err := DecryptIfNeeded(enc, "not-encrypted")
	if err != nil {
		t.Fatalf("DecryptIfNeeded (non-encrypted) failed: %v", err)
	}
	if result2 != "not-encrypted" {
		t.Error("Non-encrypted value should be returned unchanged")
	}

	// Nil encryptor with encrypted value should error
	_, err = DecryptIfNeeded(nil, encrypted)
	if err != ErrNoEncryptionKey {
		t.Errorf("Expected ErrNoEncryptionKey, got %v", err)
	}
}
