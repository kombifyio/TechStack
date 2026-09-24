package controlplane

import (
	"context"
	"errors"
	"testing"
)

func TestOnboardingStateUpsertIsACompareAndSwapOnRevision(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	base := OnboardingState{
		ID:             "ob-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Product:        "techstack",
		JourneyID:      "techstack.platform",
		JourneyVersion: 1,
		SchemaVersion:  1,
		Status:         "active",
		Revision:       0,
		State:          map[string]any{"completed": []any{}},
	}
	if _, err := store.UpsertOnboardingState(ctx, base, nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A user action from one tab moves the row to revision 1.
	first := base
	first.Revision = 1
	if _, err := store.UpsertOnboardingState(ctx, first, intPtr(0)); err != nil {
		t.Fatalf("first action: %v", err)
	}

	// A second tab acting on the revision it last saw must not win.
	second := base
	second.Revision = 1
	second.Status = "dismissed"
	_, err := store.UpsertOnboardingState(ctx, second, intPtr(0))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for a stale revision, got %v", err)
	}

	got, err := store.GetOnboardingState(ctx, "tenant-1", "auth0|user-1", "techstack", "techstack.platform")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Revision != 1 || got.Status != "active" {
		t.Fatalf("stale write leaked: revision=%d status=%s", got.Revision, got.Status)
	}
}

func intPtr(v int) *int { return &v }
