package controlplane

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreWizardRunUpsertAndGetByKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	created, err := store.UpsertWizardRun(ctx, WizardRun{
		ID:               "run-1",
		TenantID:         "tenant-1",
		OwnerSubjectID:   "auth0|user-1",
		IdempotencyKey:   "key-1",
		RequestSHA256:    "hash-1",
		RunKind:          "first-run",
		RequestedRunKind: "first-run",
		HomelabID:        "hl-1",
		StackID:          "stack-1",
		Status:           "completed",
		Result:           map[string]any{"stack_id": "stack-1"},
	})
	if err != nil {
		t.Fatalf("UpsertWizardRun: %v", err)
	}
	if created.ID != "run-1" || created.CreatedAt.IsZero() {
		t.Fatalf("unexpected created run: %#v", created)
	}

	got, err := store.GetWizardRunByKey(ctx, "tenant-1", "auth0|user-1", "key-1")
	if err != nil {
		t.Fatalf("GetWizardRunByKey: %v", err)
	}
	if got.StackID != "stack-1" || got.RequestSHA256 != "hash-1" || got.Status != "completed" {
		t.Fatalf("unexpected run: %#v", got)
	}
	if got.Result["stack_id"] != "stack-1" {
		t.Fatalf("result not persisted: %#v", got.Result)
	}
}

func TestMemoryStoreWizardRunKeyedRetryReplacesRowKeepingID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := store.UpsertWizardRun(ctx, WizardRun{
		ID: "run-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		IdempotencyKey: "key-1", RequestSHA256: "hash-1",
		RunKind: "first-run", RequestedRunKind: "first-run", Status: "failed",
		ErrorReason: "pairing failed",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	updated, err := store.UpsertWizardRun(ctx, WizardRun{
		ID: "run-2", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		IdempotencyKey: "key-1", RequestSHA256: "hash-1",
		RunKind: "first-run", RequestedRunKind: "first-run", Status: "completed",
		StackID: "stack-1",
	})
	if err != nil {
		t.Fatalf("retry upsert: %v", err)
	}
	if updated.ID != "run-1" {
		t.Fatalf("retry must keep the original ledger id, got %q", updated.ID)
	}
	if updated.Status != "completed" || updated.StackID != "stack-1" || updated.ErrorReason != "" {
		t.Fatalf("retry outcome not replaced: %#v", updated)
	}
}

func TestMemoryStoreWizardRunKeylessInsertConflictsOnlyOnID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	base := WizardRun{
		TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		RequestSHA256: "hash-1", RunKind: "expansion", RequestedRunKind: "expansion",
		Status: "completed",
	}
	first := base
	first.ID = "run-a"
	if _, err := store.UpsertWizardRun(ctx, first); err != nil {
		t.Fatalf("first keyless upsert: %v", err)
	}
	second := base
	second.ID = "run-b"
	if _, err := store.UpsertWizardRun(ctx, second); err != nil {
		t.Fatalf("second keyless upsert: %v", err)
	}
	duplicate := base
	duplicate.ID = "run-a"
	if _, err := store.UpsertWizardRun(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on duplicate id, got %v", err)
	}
}

func TestMemoryStoreWizardRunGetByKeyScopesTenantAndOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := store.UpsertWizardRun(ctx, WizardRun{
		ID: "run-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		IdempotencyKey: "key-1", RequestSHA256: "hash-1",
		RunKind: "first-run", RequestedRunKind: "first-run", Status: "completed",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.GetWizardRunByKey(ctx, "tenant-2", "auth0|user-1", "key-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read must be ErrNotFound, got %v", err)
	}
	if _, err := store.GetWizardRunByKey(ctx, "tenant-1", "auth0|user-2", "key-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read must be ErrNotFound, got %v", err)
	}
}

func TestMemoryStoreCreateStackPersistsHomelabIDAndSetStackHomelabHeals(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	linked, err := store.CreateStack(ctx, CreateStackRequest{
		ID: "stack-linked", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		HomelabID: "hl-1", Name: "Linked",
	})
	if err != nil {
		t.Fatalf("CreateStack linked: %v", err)
	}
	if linked.HomelabID != "hl-1" {
		t.Fatalf("HomelabID not persisted: %#v", linked)
	}

	legacy, err := store.CreateStack(ctx, CreateStackRequest{
		ID: "stack-legacy", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1", Name: "Legacy",
	})
	if err != nil {
		t.Fatalf("CreateStack legacy: %v", err)
	}
	if legacy.HomelabID != "" {
		t.Fatalf("legacy create must leave HomelabID empty: %#v", legacy)
	}
	healed, err := store.SetStackHomelab(ctx, "tenant-1", "stack-legacy", "hl-1")
	if err != nil {
		t.Fatalf("SetStackHomelab: %v", err)
	}
	if healed.HomelabID != "hl-1" {
		t.Fatalf("SetStackHomelab did not link: %#v", healed)
	}
	if _, err := store.SetStackHomelab(ctx, "tenant-2", "stack-legacy", "hl-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant link must be ErrNotFound, got %v", err)
	}
}
