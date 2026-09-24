// Package auth provides authentication and authorization for kombifyTechstack.
// Implements hybrid auth: mTLS for agents, JWT+Sessions for users, API keys for CLI.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ============================================================================
// Password Hashing (Argon2id - OWASP recommended)
// ============================================================================

// Argon2 parameters (OWASP recommendations for 2024)
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

// HashPassword creates an Argon2id hash of the password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	// Format: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads, saltB64, hashB64), nil
}

// VerifyPassword checks if the password matches the hash.
func VerifyPassword(password, encodedHash string) (bool, error) {
	// Parse the encoded hash
	var memory, time uint32
	var threads uint8
	var saltB64, hashB64 string

	_, err := fmt.Sscanf(encodedHash, "$argon2id$v=19$m=%d,t=%d,p=%d$%s",
		&memory, &time, &threads, &saltB64)
	if err != nil {
		return false, fmt.Errorf("invalid hash format: %w", err)
	}

	// Split salt and hash (they're separated by $)
	parts := splitLast(saltB64, '$')
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid hash format: missing components")
	}
	saltB64, hashB64 = parts[0], parts[1]

	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false, fmt.Errorf("failed to decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(hashB64)
	if err != nil {
		return false, fmt.Errorf("failed to decode hash: %w", err)
	}
	if len(expectedHash) > math.MaxUint32 {
		return false, fmt.Errorf("decoded hash is too large")
	}

	// Compute hash with same parameters
	// #nosec G115 -- len(expectedHash) is range-checked against math.MaxUint32 above.
	keyLen := uint32(len(expectedHash))
	computedHash := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)

	// Constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare(expectedHash, computedHash) == 1, nil
}

func splitLast(s string, sep byte) []string {
	if i := strings.LastIndexByte(s, sep); i >= 0 {
		return []string{s[:i], s[i+1:]}
	}
	return []string{s}
}
