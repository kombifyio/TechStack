// Package auth provides authentication and security utilities for kombifyTechstack.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	// EncryptionKeyEnvVar is the environment variable name for the encryption key.
	EncryptionKeyEnvVar = "TECHSTACK_ENCRYPTION_KEY"

	// EncryptedPrefix is prepended to encrypted values for identification.
	EncryptedPrefix = "enc:v1:"
)

var (
	// ErrNoEncryptionKey is returned when encryption is attempted without a key.
	ErrNoEncryptionKey = errors.New("encryption key not configured (set TECHSTACK_ENCRYPTION_KEY)")

	// ErrInvalidCiphertext is returned when decryption fails.
	ErrInvalidCiphertext = errors.New("invalid or corrupted ciphertext")

	// ErrKeyTooShort is returned when the encryption key is not 32 bytes.
	ErrKeyTooShort = errors.New("encryption key must be exactly 32 bytes (256-bit)")
)

// SecretEncryptor handles encryption/decryption of sensitive data using AES-256-GCM.
// It is safe for concurrent use.
type SecretEncryptor struct {
	key []byte
	mu  sync.RWMutex
}

// globalEncryptor is the default encryptor instance.
var globalEncryptor *SecretEncryptor
var encryptorOnce sync.Once

// GetEncryptor returns the global SecretEncryptor instance.
// It initializes from TECHSTACK_ENCRYPTION_KEY environment variable on first call.
func GetEncryptor() *SecretEncryptor {
	encryptorOnce.Do(func() {
		keyStr := os.Getenv(EncryptionKeyEnvVar)
		if keyStr != "" {
			enc, err := NewSecretEncryptor([]byte(keyStr))
			if err == nil {
				globalEncryptor = enc
			}
		}
	})
	return globalEncryptor
}

// NewSecretEncryptor creates a new encryptor with the given 32-byte key.
func NewSecretEncryptor(key []byte) (*SecretEncryptor, error) {
	if len(key) != 32 {
		return nil, ErrKeyTooShort
	}
	return &SecretEncryptor{key: key}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns base64-encoded ciphertext prefixed with "enc:v1:" for identification.
func (e *SecretEncryptor) Encrypt(plaintext string) (string, error) {
	if e == nil || len(e.key) == 0 {
		return "", ErrNoEncryptionKey
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and prepend nonce to ciphertext
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode as base64 with version prefix
	return EncryptedPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts ciphertext encrypted with Encrypt.
// Expects input in format "enc:v1:<base64-encoded-ciphertext>".
func (e *SecretEncryptor) Decrypt(ciphertext string) (string, error) {
	if e == nil || len(e.key) == 0 {
		return "", ErrNoEncryptionKey
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Check and strip prefix
	if len(ciphertext) < len(EncryptedPrefix) || ciphertext[:len(EncryptedPrefix)] != EncryptedPrefix {
		return "", ErrInvalidCiphertext
	}
	ciphertext = ciphertext[len(EncryptedPrefix):]

	// Decode base64
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", ErrInvalidCiphertext
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}

	return string(plaintext), nil
}

// IsEncrypted checks if a value is already encrypted (has the enc:v1: prefix).
func IsEncrypted(value string) bool {
	return len(value) >= len(EncryptedPrefix) && value[:len(EncryptedPrefix)] == EncryptedPrefix
}

// EncryptIfNeeded encrypts a value only if it's not already encrypted.
// Returns the original value unchanged if encryptor is nil.
func EncryptIfNeeded(e *SecretEncryptor, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if IsEncrypted(value) {
		return value, nil // Already encrypted
	}
	if e == nil {
		return value, nil // No encryption available, return as-is
	}
	return e.Encrypt(value)
}

// DecryptIfNeeded decrypts a value only if it's encrypted.
// Returns the original value unchanged if not encrypted or if encryptor is nil.
func DecryptIfNeeded(e *SecretEncryptor, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !IsEncrypted(value) {
		return value, nil // Not encrypted, return as-is
	}
	if e == nil {
		return "", ErrNoEncryptionKey
	}
	return e.Decrypt(value)
}
