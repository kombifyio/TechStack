package db

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/google/uuid"
)

// TestIntegrationOperatorSelfDisclosureIsPrincipalScoped guards the privacy
// boundary of the voluntary self-disclosure: the latest answers replace the
// earlier ones for the same principal, and no other tenant or subject can
// read them.
func TestIntegrationOperatorSelfDisclosureIsPrincipalScoped(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	suffix := uuid.NewString()
	tenantID := "disclosure-tenant-" + suffix
	otherTenantID := "disclosure-other-tenant-" + suffix
	for _, id := range []string{tenantID, otherTenantID} {
		if _, err := store.EnsureTenant(ctx, controlplane.Tenant{ID: id}); err != nil {
			t.Fatalf("EnsureTenant(%s): %v", id, err)
		}
	}

	write := func(experience string) {
		t.Helper()
		if _, err := store.UpsertOperatorSelfDisclosure(ctx, controlplane.OperatorSelfDisclosure{
			ID: uuid.NewString(), TenantID: tenantID, OwnerSubjectID: "owner-1",
			Source:     controlplane.OperatorSelfDisclosureSourceCloud,
			Disclosure: core.OperatorSelfDisclosure{Experience: experience, Goals: []string{"photos"}, Placement: "home"},
		}); err != nil {
			t.Fatalf("UpsertOperatorSelfDisclosure(%s): %v", experience, err)
		}
	}
	write(core.SelfDisclosureExperienceGuided)
	write(core.SelfDisclosureExperienceTechie)

	got, err := store.GetOperatorSelfDisclosure(ctx, tenantID, "owner-1")
	if err != nil {
		t.Fatalf("GetOperatorSelfDisclosure: %v", err)
	}
	if got.Disclosure.Experience != core.SelfDisclosureExperienceTechie || got.Disclosure.Placement != "home" {
		t.Fatalf("stored disclosure = %#v, want the latest answers", got.Disclosure)
	}

	for _, scope := range [][2]string{{tenantID, "owner-2"}, {otherTenantID, "owner-1"}} {
		if _, err := store.GetOperatorSelfDisclosure(ctx, scope[0], scope[1]); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatalf("GetOperatorSelfDisclosure(%s, %s) error = %v, want ErrNotFound", scope[0], scope[1], err)
		}
	}
}
