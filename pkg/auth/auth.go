// Package auth provides authentication and authorization for kombifyTechstack.
// Implements hybrid auth: mTLS for agents, JWT+Sessions for users, API keys for CLI.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

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

// ============================================================================
// API Key Generation
// ============================================================================

const (
	apiKeyPrefix    = "ks_" // kombifyTechstack API key prefix
	apiKeyLen       = 32    // Random bytes for key
	apiKeyPrefixLen = 8     // Visible prefix for identification
)

// APIKey represents a generated API key with its components.
type APIKey struct {
	// Key is the full key to give to the user (only shown once)
	Key string
	// Prefix is the first 8 chars for identification (ks_abc123)
	Prefix string
	// Hash is SHA-256 hash to store in database
	Hash string
}

// GenerateAPIKey creates a new API key.
func GenerateAPIKey() (*APIKey, error) {
	// Generate random bytes
	keyBytes := make([]byte, apiKeyLen)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	// Create the full key
	keyStr := base64.RawURLEncoding.EncodeToString(keyBytes)
	fullKey := apiKeyPrefix + keyStr

	// Create visible prefix (first 8 chars after ks_)
	prefix := fullKey[:len(apiKeyPrefix)+apiKeyPrefixLen]

	// Hash the full key for storage
	hashBytes := sha256.Sum256([]byte(fullKey))
	hash := hex.EncodeToString(hashBytes[:])

	return &APIKey{
		Key:    fullKey,
		Prefix: prefix,
		Hash:   hash,
	}, nil
}

// VerifyAPIKey checks if the provided key matches the stored hash.
func VerifyAPIKey(key, storedHash string) bool {
	hashBytes := sha256.Sum256([]byte(key))
	computedHash := hex.EncodeToString(hashBytes[:])
	return subtle.ConstantTimeCompare([]byte(computedHash), []byte(storedHash)) == 1
}

// GetAPIKeyPrefix extracts the prefix from a full API key.
func GetAPIKeyPrefix(key string) string {
	if len(key) < len(apiKeyPrefix)+apiKeyPrefixLen {
		return ""
	}
	return key[:len(apiKeyPrefix)+apiKeyPrefixLen]
}

// ============================================================================
// Session Tokens
// ============================================================================

const sessionTokenLen = 32

// GenerateSessionToken creates a cryptographically secure session token.
func GenerateSessionToken() (token string, hash string, err error) {
	tokenBytes := make([]byte, sessionTokenLen)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate token: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(tokenBytes)
	hashBytes := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(hashBytes[:])

	return token, hash, nil
}

// HashSessionToken creates a SHA-256 hash of a session token.
func HashSessionToken(token string) string {
	hashBytes := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hashBytes[:])
}

// ============================================================================
// JWT Claims (for stateless auth when needed)
// ============================================================================

// Claims represents JWT token claims.
type Claims struct {
	UserID    string    `json:"sub"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`
}

// Note: JWT signing/verification should use a proper library like golang-jwt/jwt
// This is just the claims structure. Implementation requires:
// go get github.com/golang-jwt/jwt/v5
