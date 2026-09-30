package main

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
)

type qualificationMemoryStore struct{ *controlplane.MemoryStore }

func (s qualificationMemoryStore) UpsertTenant(_ context.Context, tenant controlplane.Tenant) (*controlplane.Tenant, error) {
	return &tenant, nil
}

func TestOperatorQualificationFixtureRequiresAttemptDatabase(t *testing.T) {
	const attempt = "a3d1b5c7-3344-4a01-a943-066d5dcd0f3a"
	exact := db.Config{Backend: db.StoreBackendPostgres, DSN: "postgres://techstack:secret@127.0.0.1:55432/techstack_qualification_a3d1b5c733444a01a943066d5dcd0f3a?sslmode=disable"}
	if err := requireLocalQualificationDatabase(exact, attempt); err != nil {
		t.Fatalf("exact attempt database rejected: %v", err)
	}
	for _, dsn := range []string{
		"postgres://techstack:secret@127.0.0.1:55432/techstack?sslmode=disable",
		"postgres://techstack:secret@db.internal:5432/techstack_qualification_a3d1b5c733444a01a943066d5dcd0f3a?sslmode=disable",
	} {
		if err := requireLocalQualificationDatabase(db.Config{Backend: db.StoreBackendPostgres, DSN: dsn}, attempt); err == nil {
			t.Fatalf("unsafe qualification database accepted: %s", dsn)
		}
	}
}

func (s qualificationMemoryStore) UpsertUser(_ context.Context, user controlplane.User) (*controlplane.User, error) {
	return &user, nil
}

func (s qualificationMemoryStore) UpsertMembership(_ context.Context, membership controlplane.Membership) (*controlplane.Membership, error) {
	return &membership, nil
}

func TestOperatorQualificationFixtureSeedsPlannedCloudWithoutLifecycleJob(t *testing.T) {
	const (
		attempt = "a3d1b5c7-3344-4a01-a943-066d5dcd0f3a"
		tenant  = "qualification-tenant"
	)
	store := qualificationMemoryStore{MemoryStore: controlplane.NewMemoryStore()}
	result, err := seedOperatorQualificationFixture(t.Context(), store, operatorQualificationFixture{
		Attempt: attempt, TenantID: tenant, Provider: "ionos", Region: "de-fra",
	})
	if err != nil {
		t.Fatalf("seedOperatorQualificationFixture: %v", err)
	}
	stack, err := store.GetStack(t.Context(), tenant, result.StackID)
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	userConfig, _ := stack.Config["user_config"].(map[string]any)
	if stack.Status != "draft" || stack.OwnerSubjectID != result.OwnerSubjectID ||
		userConfig["stackkit"] != "cloud-kit" || userConfig["server_provisioning_mode"] != "kombify-cloud" ||
		userConfig["runtime_offering_id"] != "monthly-runtime-premium" || result.AutoDeploy {
		t.Fatalf("fixture seeded non-planned or non-Cloud state: stack=%#v result=%#v", stack, result)
	}
	jobs, err := store.ListJobsByTenant(t.Context(), tenant, 10)
	if err != nil {
		t.Fatalf("ListJobsByTenant: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("fixture scheduled lifecycle jobs before native admission: %#v", jobs)
	}

	replayed, err := seedOperatorQualificationFixture(t.Context(), store, operatorQualificationFixture{
		Attempt: attempt, TenantID: tenant, Provider: "ionos", Region: "de-fra",
	})
	if err != nil {
		t.Fatalf("replay seedOperatorQualificationFixture: %v", err)
	}
	if replayed.StackID != result.StackID || replayed.IdempotencyKey != result.IdempotencyKey {
		t.Fatalf("fixture replay changed authority: first=%#v replay=%#v", result, replayed)
	}
}
