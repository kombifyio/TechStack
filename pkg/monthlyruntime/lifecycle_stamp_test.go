package monthlyruntime

import (
	"testing"
	"time"
)

func TestStampEnrollmentStart(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)

	// Sets the stamp when absent.
	m := map[string]string{}
	StampEnrollmentStart(m, now)
	if got := m[EnrollmentStartedAtKey]; got != "2026-05-12T12:00:00Z" {
		t.Fatalf("stamp = %q, want RFC3339 UTC", got)
	}

	// Never overwrites an existing stamp (age is from first creation).
	StampEnrollmentStart(m, now.Add(time.Hour))
	if got := m[EnrollmentStartedAtKey]; got != "2026-05-12T12:00:00Z" {
		t.Fatalf("stamp overwritten = %q, want original", got)
	}

	// Blank existing stamp is (re)filled.
	m2 := map[string]string{EnrollmentStartedAtKey: "  "}
	StampEnrollmentStart(m2, now)
	if got := m2[EnrollmentStartedAtKey]; got != "2026-05-12T12:00:00Z" {
		t.Fatalf("blank stamp = %q, want filled", got)
	}

	// Nil-safe.
	StampEnrollmentStart(nil, now)
}
