package stacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestPairingTokenResultStillValidAcceptsBothShapes(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if !pairingTokenResultStillValid(now.Add(10*time.Minute), now) {
		t.Fatal("future time.Time must be valid")
	}
	if pairingTokenResultStillValid(now.Add(-time.Second), now) {
		t.Fatal("expired time.Time must be invalid")
	}
	if !pairingTokenResultStillValid(now.Add(10*time.Minute).Format(time.RFC3339Nano), now) {
		t.Fatal("future RFC3339 string must be valid")
	}
	if pairingTokenResultStillValid("not-a-timestamp", now) {
		t.Fatal("unparseable expiry must be invalid")
	}
	if pairingTokenResultStillValid(nil, now) {
		t.Fatal("missing expiry must be invalid")
	}
}

func TestRemoteEnrollmentPairingTokenReusesValidRowTokenAndRemintsWhenExpired(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	handlers := wizardRunHandlers{
		cfg: WizardRunRouteConfig{
			Trust: trust.RouteStores{Jobs: store, Workers: store},
		},
	}
	stack := &controlplane.Stack{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Demo"}

	// No durable row yet: a fresh token-only capability is minted.
	minted, err := handlers.remoteEnrollmentPairingToken(ctx, "tenant-1", "job-missing", "owner-1", stack)
	if err != nil {
		t.Fatalf("mint without row: %v", err)
	}
	if !strings.HasPrefix(minted, "kpt1.") {
		t.Fatalf("minted token = %q, want the kpt1 wire shape", minted)
	}

	// A durable row with a still-valid token is reused verbatim.
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-valid", TenantID: "tenant-1", StackID: "stack-1", Type: "remote_enrollment",
		State: "pending", Result: map[string]any{
			"registration_token": "kpt1.reused-capability",
			"token_expires_at":   time.Now().UTC().Add(10 * time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	reused, err := handlers.remoteEnrollmentPairingToken(ctx, "tenant-1", "job-valid", "owner-1", stack)
	if err != nil {
		t.Fatalf("reuse valid row token: %v", err)
	}
	if reused != "kpt1.reused-capability" {
		t.Fatalf("token = %q, want the durable row's token", reused)
	}

	// An expired row token must not be reused: a fresh capability is minted.
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-expired", TenantID: "tenant-1", StackID: "stack-1", Type: "remote_enrollment",
		State: "pending", Result: map[string]any{
			"registration_token": "kpt1.expired-capability",
			"token_expires_at":   time.Now().UTC().Add(-time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := handlers.remoteEnrollmentPairingToken(ctx, "tenant-1", "job-expired", "owner-1", stack)
	if err != nil {
		t.Fatalf("remint expired row token: %v", err)
	}
	if fresh == "kpt1.expired-capability" || !strings.HasPrefix(fresh, "kpt1.") {
		t.Fatalf("token = %q, want a freshly minted capability", fresh)
	}
}
