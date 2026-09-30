package db

import (
	"maps"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/google/uuid"
)

func TestIntegrationManagedLeasePinsFirstGuardSSHHostKeyWithoutLegacyMetadataMutation(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	tenantID, ownerID := "ssh-pin-tenant-"+suffix, "ssh-pin-owner-"+suffix
	stackID, serverID := "ssh-pin-stack-"+suffix, "ssh-pin-server-"+suffix
	leaseID := vmlease.LeaseID("ssh-pin-lease-" + suffix)

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ($1, $1, 'saas', 'active')
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status)
		VALUES ($1, $2, $3, $1, 'active')
	`, stackID, tenantID, ownerID); err != nil {
		t.Fatal(err)
	}
	store := vmleases.NewPostgresStore(database.DB)
	admitted, err := store.AdmitNativeLeaseTx(t.Context(), tx, vmleases.NativeAdmissionRequest{
		Lease: vmlease.Lease{
			ID: leaseID, Subject: vmlease.Subject{Kind: vmlease.SubjectOrg, ID: tenantID, OrgID: tenantID},
			Resource: vmlease.ResourceRef{ProviderID: "centron"}, DesiredState: vmlease.DesiredStateRunning,
			BillingMode: vmlease.BillingModeSubscription, LifecycleClass: vmlease.LifecycleClassSubscription,
			RestartPolicy: vmlease.RestartPolicyOnUnexpectedStop, RecreatePolicy: vmlease.RecreatePolicyNever,
		},
		OwnerSubjectID: ownerID, ServerID: serverID, IdempotencyKey: "ssh-pin-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO servers (
			id, tenant_id, stack_id, owner_subject_id, lease_id, name,
			lifecycle_state, desired_state, connection_state, health_state,
			inventory_revision, revision, generation
		) VALUES ($1,$2,$3,$4,$5,$1,'active','running','connected','healthy',1,1,1)
	`, serverID, tenantID, stackID, ownerID, leaseID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	stale, err := store.Get(t.Context(), tenantID, leaseID)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := *stale
	unauthorized.Metadata = maps.Clone(stale.Metadata)
	unauthorized.Metadata[vmleases.MetadataKeySSHHostKey] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIES1Xflf/Yf/edLYoabUDw1v88bOXzegNvZyNiiH+bik"
	if _, err := store.Update(t.Context(), tenantID, unauthorized); err != vmleases.ErrSSHHostKeyImmutable {
		t.Fatalf("ordinary first-pin update error = %v, want %v", err, vmleases.ErrSSHHostKeyImmutable)
	}
	digest, err := vmleases.ResourceGenerationDigest(tenantID, admitted.Lease)
	if err != nil {
		t.Fatal(err)
	}
	request := vmleases.SSHHostKeyPinRequest{
		TenantID: tenantID, LeaseID: leaseID, OwnerSubjectID: ownerID,
		ServerID: serverID, StackID: stackID, ServerGeneration: 1,
		ExpectedResourceGenerationDigest: digest,
		HostKey:                          "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIES1Xflf/Yf/edLYoabUDw1v88bOXzegNvZyNiiH+bik",
	}
	service := vmleases.NewService(store, vmleases.ServiceConfig{})
	pinned, err := service.PinSSHHostKey(t.Context(), request)
	if err != nil {
		t.Fatalf("pin through production migration boundary: %v", err)
	}
	if pinned.Metadata[vmleases.MetadataKeySSHHostKey] != request.HostKey || pinned.Metadata["runtime_enrollment_status"] != "" {
		t.Fatalf("pinned lease metadata = %#v", pinned.Metadata)
	}
	request.HostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILqyqJcqqaM5qWF7+Qgk8UmVXBNhVCGLyHpkvh043crA"
	unchanged, err := service.PinSSHHostKey(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Metadata[vmleases.MetadataKeySSHHostKey] != pinned.Metadata[vmleases.MetadataKeySSHHostKey] {
		t.Fatalf("later Guard key replaced first pin: %#v", unchanged.Metadata)
	}
	stale.Metadata["unrelated"] = "stale-update"
	if _, err := store.Update(t.Context(), tenantID, *stale); err != vmleases.ErrSSHHostKeyImmutable {
		t.Fatalf("stale whole-lease update error = %v, want %v", err, vmleases.ErrSSHHostKeyImmutable)
	}
}
