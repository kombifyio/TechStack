package localdb

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: the embedded-postgres library wipes a data directory whose
// PG_VERSION differs from the bundled major. The guard must fail first and
// leave the owner's data untouched.
func TestIncompatibleDataDirFailsWithoutTouchingData(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(data, "base-file")
	if err := os.WriteFile(marker, []byte("owner data"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, version := range map[string]string{"newer major": "17\n", "unrecognized": ""} {
		if version != "" {
			if err := os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte(version), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := ensureDataDirCompatible(data); err == nil {
			t.Fatalf("%s: expected the guard to refuse", name)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("%s: data was touched: %v", name, err)
		}
	}

	if err := os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDataDirCompatible(data); err != nil {
		t.Fatalf("bundled major must be accepted: %v", err)
	}
	if err := ensureDataDirCompatible(filepath.Join(t.TempDir(), "fresh")); err != nil {
		t.Fatalf("fresh install must be accepted: %v", err)
	}
}
