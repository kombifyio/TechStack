package db

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/google/uuid"
)

// TestIntegrationJobsAdmitRemoteEnrollmentType guards the schema contract the
// connect-remote enrollment mint depends on. createStoreRemoteEnrollmentJob
// writes type='remote_enrollment'; if the jobs type check does not admit it,
// the wizard fails closed before any SSH work starts.
func TestIntegrationJobsAdmitRemoteEnrollmentType(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	suffix := uuid.NewString()
	tenantID := "enrollment-tenant-" + suffix
	stackID := "enrollment-stack-" + suffix
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: stackID, TenantID: tenantID, OwnerSubjectID: "owner-1",
		Name: "Enrollment " + suffix, Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	job, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "enrollment-job-" + suffix, TenantID: tenantID, StackID: stackID,
		Type: "remote_enrollment", State: "pending",
		Step: "remote_ssh_connect", Message: "Connecting to your Node over SSH…",
	})
	if err != nil {
		t.Fatalf("remote_enrollment job must be admitted by the jobs type check: %v", err)
	}
	if job.Type != "remote_enrollment" || job.State != "pending" {
		t.Fatalf("job = %#v, want a pending remote_enrollment row", job)
	}
}
