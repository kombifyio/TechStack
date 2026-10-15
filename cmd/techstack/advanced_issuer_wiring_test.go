package main

import (
	"testing"

	"github.com/kombifyio/techstack/internal/advancedissuer"
)

// Production without governed signing preparation must keep the application
// custody it already runs with (ix4h.1 bridge); a configured binding or the
// explicit switch selects bound custody.
func TestAdvancedIssuerCustodyMode(t *testing.T) {
	cases := []struct{ mode, binding, want string }{
		{"", "", advancedIssuerCustodyApplication},
		{"", "postgresql://signing@db.invalid/runtime?sslmode=verify-full", advancedIssuerCustodyBound},
		{"bound", "", advancedIssuerCustodyBound},
		{"application", "postgresql://signing@db.invalid/runtime?sslmode=verify-full", advancedIssuerCustodyApplication},
	}
	for _, c := range cases {
		t.Setenv(envAdvancedIssuerCustody, c.mode)
		t.Setenv(advancedissuer.EnvKeyCustodyDatabaseURL, c.binding)
		if got, err := advancedIssuerCustodyMode(); err != nil || got != c.want {
			t.Fatalf("mode %q binding %q: got %q, %v; want %q", c.mode, c.binding, got, err, c.want)
		}
	}
	t.Setenv(envAdvancedIssuerCustody, "other")
	if _, err := advancedIssuerCustodyMode(); err == nil {
		t.Fatal("unknown custody mode must be refused")
	}
}
