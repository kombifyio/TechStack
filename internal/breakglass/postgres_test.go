package breakglass

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/go-common/authlocal"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestPostgresStoreSavesBreakglassRecordInAuthStore(t *testing.T) {
	ctx := context.Background()
	authStore := &fakeAuthStore{}
	store := NewPostgres(authStore, "tenant-1")
	showUntil := time.Date(2026, 5, 28, 17, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, &authlocal.Record{
		Email:        "breakglass@techstack.local",
		PasswordHash: "$2a$hash",
		ShowPassword: "show-once",
		ShowUntil:    showUntil,
		Claimed:      false,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	admin := authStore.admin
	if admin.TenantID != "tenant-1" || admin.ID != authlocal.BreakGlassRecordID {
		t.Fatalf("unexpected admin identity: %#v", admin)
	}
	if admin.Email != "breakglass@techstack.local" || admin.PasswordHash != "$2a$hash" {
		t.Fatalf("unexpected admin credentials: %#v", admin)
	}
	if admin.Metadata[breakglassMetadataShowPassword] != "show-once" {
		t.Fatalf("show password metadata not persisted: %#v", admin.Metadata)
	}
	if admin.Metadata[breakglassMetadataShowUntil] != showUntil.Format(time.RFC3339) {
		t.Fatalf("show until metadata not persisted: %#v", admin.Metadata)
	}
}

func TestPostgresStoreReadsBreakglassRecordFromAuthStore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 28, 17, 15, 0, 0, time.UTC)
	authStore := &fakeAuthStore{
		admin: &controlplane.BreakglassAdmin{
			ID:           authlocal.BreakGlassRecordID,
			TenantID:     "tenant-1",
			Email:        "operator@example.test",
			PasswordHash: "$2a$hash",
			Metadata: map[string]any{
				breakglassMetadataClaimed:      true,
				breakglassMetadataShowPassword: "show-once",
				breakglassMetadataShowUntil:    now.Format(time.RFC3339),
			},
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
	}

	got, err := NewPostgres(authStore, "tenant-1").Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "operator@example.test" || got.PasswordHash != "$2a$hash" || !got.Claimed {
		t.Fatalf("unexpected record: %#v", got)
	}
	if got.ShowPassword != "show-once" || !got.ShowUntil.Equal(now) {
		t.Fatalf("unexpected reveal envelope: %#v", got)
	}
}

type fakeAuthStore struct {
	admin *controlplane.BreakglassAdmin
}

func (f *fakeAuthStore) UpsertTenant(context.Context, controlplane.Tenant) (*controlplane.Tenant, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) UpsertUser(context.Context, controlplane.User) (*controlplane.User, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) UpsertMembership(context.Context, controlplane.Membership) (*controlplane.Membership, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) GetMembership(context.Context, string, string) (*controlplane.Membership, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) ListMembershipsByUser(context.Context, string) ([]controlplane.Membership, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) UpsertAuthConfig(context.Context, controlplane.AuthConfig) (*controlplane.AuthConfig, error) {
	panic("not implemented")
}

func (f *fakeAuthStore) UpsertBreakglassAdmin(_ context.Context, admin controlplane.BreakglassAdmin) (*controlplane.BreakglassAdmin, error) {
	stored := admin
	f.admin = &stored
	return &stored, nil
}

func (f *fakeAuthStore) GetBreakglassAdmin(_ context.Context, tenantID string) (*controlplane.BreakglassAdmin, error) {
	if f.admin == nil || f.admin.TenantID != tenantID {
		return nil, controlplane.ErrNotFound
	}
	stored := *f.admin
	return &stored, nil
}
