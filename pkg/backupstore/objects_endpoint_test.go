package backupstore

import "testing"

// TestPurgerRefusesAJurisdictionMismatch covers a live configuration hazard:
// R2_ENDPOINT is a shared operator override and is currently set to the
// default jurisdiction, while managed backup buckets are created in eu. If the
// override won silently the purger would list an empty bucket set and the wipe
// would report success while leaving every customer object in place.
func TestPurgerRefusesAJurisdictionMismatch(t *testing.T) {
	t.Setenv("R2_ACCESS_KEY_ID", "key")
	t.Setenv("R2_SECRET_ACCESS_KEY", "secret")

	t.Setenv("R2_ENDPOINT", "https://acct123.r2.cloudflarestorage.com")
	if _, err := S3PurgerFromEnv("acct123", JurisdictionEU); err == nil {
		t.Fatal("a default-jurisdiction R2_ENDPOINT must be refused for an eu purger")
	}

	// The same override is correct when it agrees with the jurisdiction, which
	// is what legacy cleanup needs.
	if _, err := S3PurgerFromEnv("acct123", JurisdictionDefault); err != nil {
		t.Fatalf("matching override must be accepted: %v", err)
	}

	t.Setenv("R2_ENDPOINT", "https://acct123.eu.r2.cloudflarestorage.com/")
	if _, err := S3PurgerFromEnv("acct123", JurisdictionEU); err != nil {
		t.Fatalf("matching eu override must be accepted despite a trailing slash: %v", err)
	}

	// Unset falls back to the derived endpoint for the jurisdiction.
	t.Setenv("R2_ENDPOINT", "")
	if _, err := S3PurgerFromEnv("acct123", JurisdictionEU); err != nil {
		t.Fatalf("unset override must derive the endpoint: %v", err)
	}
}
