package db

import (
	"errors"
	"testing"
)

// Sensitive migration boundary: an unexpected or already committed ledger is
// inspected, never silently resumed. This models admission data, not a database.
func TestForwardBatchRejectsChangedLedger(t *testing.T) {
	batch, err := DeliveryForwardBatch()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, artifact := range batch {
		pending[artifact.Version] = true
	}
	basis := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && !pending[entry.Name()] {
			basis = append(basis, entry.Name())
		}
	}
	check := func(applied []string) error {
		expected := map[string]bool{}
		for _, version := range basis {
			expected[version] = true
		}
		return compareForwardBatchLedger(expected, applied)
	}
	if err := check(basis); err != nil {
		t.Fatal(err)
	}
	for _, applied := range [][]string{append(append([]string{}, basis...), batch[0].Version), append(append([]string{}, basis...), "unexpected.sql"), basis[1:]} {
		if err := check(applied); !errors.Is(err, ErrForwardBatchLedger) {
			t.Fatalf("changed ledger admitted: %v", err)
		}
	}
}
