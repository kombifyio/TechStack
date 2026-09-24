package routes

import (
	"os"
	"testing"

	"github.com/kombifyio/techstack/pkg/auth"
)

// TestMain configures wallet custody for the package before the global
// encryptor initializes. Secret-bearing wallet writes fail closed without a
// configured key, and the package exercises those write paths.
func TestMain(m *testing.M) {
	const testKey = "routes-pkg-key-exactly-32-bytes!"
	if len(testKey) != 32 {
		panic("wallet encryption test key must be exactly 32 bytes")
	}
	os.Setenv(auth.EncryptionKeyEnvVar, testKey)
	os.Exit(m.Run())
}
