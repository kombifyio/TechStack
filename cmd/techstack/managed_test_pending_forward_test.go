package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

func TestPendingForwardRefusesUnboundArtifactBeforeDatabaseEffects(t *testing.T) {
	for _, purpose := range []struct{ source, intent string }{
		{"", strings.Repeat("a", 64)},
		{strings.Repeat("b", 40), strings.Repeat("a", 64)},
		{buildRevision, ""},
		{buildRevision, strings.Repeat("z", 64)},
	} {
		t.Setenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA", purpose.source)
		t.Setenv("TECHSTACK_MANAGED_TEST_PENDING_FORWARD_INTENT_SHA256", purpose.intent)
		if err := runManagedTestPendingForward(context.Background(), db.ManagedTestAssignment{}, nil, strings.Repeat("a", 64)); !errors.Is(err, errManagedTestPendingForwardAdmission) {
			t.Fatalf("unbound migration purpose was admitted: %v", err)
		}
	}
}
