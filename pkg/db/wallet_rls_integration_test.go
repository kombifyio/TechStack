package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestIntegrationWalletMigrationEnforcesTenantIdentity(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runWalletWritePreservesImmutableIdentity(t, controlplane.NewMemoryStore(), walletIdentityFixtures{}); err != nil {
		t.Fatal(err)
	}

	var schema string
	if err := database.QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := "wallet_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := pgx.Identifier{role}.Sanitize()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := database.ExecContext(t.Context(), "CREATE ROLE "+quotedRole+" LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create scoped Wallet runtime role: %v", err)
	}
	t.Cleanup(func() {
		if _, err := database.ExecContext(context.Background(), "DROP OWNED BY "+quotedRole); err != nil {
			t.Errorf("revoke scoped Wallet runtime role grants: %v", err)
		}
		if _, err := database.ExecContext(context.Background(), "DROP ROLE "+quotedRole); err != nil {
			t.Errorf("drop scoped Wallet runtime role: %v", err)
		}
	})
	if _, err := database.ExecContext(t.Context(), "GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE "+quotedSchema+`.wallet_items TO `+quotedRole); err != nil {
		t.Fatal(err)
	}

	runtimeConfig, err := pgx.ParseConfig(strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL")))
	if err != nil {
		t.Fatal("parse test PostgreSQL configuration")
	}
	runtimeConfig.User = role
	runtimeConfig.Password = password
	runtimeDB := stdlib.OpenDB(*runtimeConfig)
	runtimeDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = runtimeDB.Close() })
	if err := runtimeDB.PingContext(t.Context()); err != nil {
		t.Fatal("connect as scoped Wallet runtime role")
	}
	var isSuperuser, bypassesRLS, rowSecurityActive bool
	if err := runtimeDB.QueryRowContext(t.Context(), `
		SELECT role.rolsuper, role.rolbypassrls, row_security_active($1::regclass)
		FROM pg_roles AS role WHERE role.rolname = current_user
	`, schema+".wallet_items").Scan(&isSuperuser, &bypassesRLS, &rowSecurityActive); err != nil {
		t.Fatal(err)
	}
	if isSuperuser || bypassesRLS || !rowSecurityActive {
		t.Fatalf("Wallet runtime role does not exercise row security: superuser=%t bypass=%t active=%t", isSuperuser, bypassesRLS, rowSecurityActive)
	}

	if err := runWalletWritePreservesImmutableIdentity(t, controlplane.NewPostgresStore(runtimeDB), walletIdentityFixtures{
		seedLegacy: func(ctx context.Context, item controlplane.WalletItem) error {
			return seedLegacyWalletItemWithTenantScope(ctx, runtimeDB, schema, item)
		},
		seedInstance: func(ctx context.Context, tenantID, instanceID string) error {
			return seedWalletInstanceWithTenantScope(ctx, database, schema, tenantID, instanceID)
		},
		deleteInstance: func(ctx context.Context, tenantID, instanceID string) error {
			return deleteWalletInstanceWithTenantScope(ctx, database, schema, tenantID, instanceID)
		},
	}); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	alpha := controlplane.WalletItem{ID: "wallet-alpha-" + suffix, TenantID: "tenant-alpha-" + suffix, ItemType: "ssh_key",
		Metadata: map[string]any{"owner_id": "owner-alpha", "secret": "synthetic-alpha"}}
	beta := controlplane.WalletItem{ID: "wallet-beta-" + suffix, TenantID: "tenant-beta-" + suffix, ItemType: "ssh_key",
		Metadata: map[string]any{"owner_id": "owner-beta", "secret": "synthetic-beta"}}
	store := controlplane.NewPostgresStore(runtimeDB)
	if _, err := store.CreateWalletItem(t.Context(), alpha); err != nil {
		t.Fatalf("create tenant alpha Wallet item: %v", err)
	}
	t.Cleanup(func() { assertWalletCleanup(t, store, alpha) })
	if _, err := store.CreateWalletItem(t.Context(), beta); err != nil {
		t.Fatalf("create tenant beta Wallet item: %v", err)
	}
	t.Cleanup(func() { assertWalletCleanup(t, store, beta) })

	if _, err := store.GetWalletItem(t.Context(), alpha.TenantID, beta.ID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("tenant alpha read tenant beta item: %v", err)
	}
	if _, err := store.UpdateWalletItem(t.Context(), controlplane.WalletItem{
		ID: beta.ID, TenantID: alpha.TenantID, ItemType: beta.ItemType,
		Metadata: map[string]any{"owner_id": "owner-beta", "secret": "synthetic-attacker"},
	}); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("tenant alpha update of tenant beta item: %v", err)
	}
	var unscopedRows int
	if err := runtimeDB.QueryRowContext(t.Context(), `SELECT count(*) FROM wallet_items`).Scan(&unscopedRows); err != nil {
		t.Fatal(err)
	}
	if unscopedRows != 0 {
		t.Fatalf("unscoped runtime query returned %d rows", unscopedRows)
	}
}

type walletIdentityFixtures struct {
	seedLegacy     func(context.Context, controlplane.WalletItem) error
	seedInstance   func(context.Context, string, string) error
	deleteInstance func(context.Context, string, string) error
}

func runWalletWritePreservesImmutableIdentity(
	t *testing.T,
	store controlplane.WalletStore,
	fixtures walletIdentityFixtures,
) error {
	t.Helper()
	suffix := uuid.NewString()
	tenantID, instanceID := "tenant-a-"+suffix, "instance-a-"+suffix
	if fixtures.seedInstance != nil {
		if err := fixtures.seedInstance(t.Context(), tenantID, instanceID); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := fixtures.deleteInstance(context.Background(), tenantID, instanceID); err != nil {
				t.Errorf("clean Wallet test instance: %v", err)
			}
		})
	}
	original := controlplane.WalletItem{ID: "wallet-fixed-" + suffix, TenantID: tenantID, InstanceID: instanceID, ItemType: "ssh_key",
		Metadata: map[string]any{"owner_subject_id": "owner-a", "secret": "synthetic-original"}}
	if _, err := store.CreateWalletItem(t.Context(), original); err != nil {
		return err
	}
	t.Cleanup(func() { assertWalletCleanup(t, store, original) })
	assertOriginal := func(t *testing.T) {
		t.Helper()
		got, err := store.GetWalletItem(t.Context(), original.TenantID, original.ID)
		if err != nil || controlplane.WalletOwnerID(got.Metadata) != "owner-a" || got.ItemType != "ssh_key" || got.Metadata["secret"] != "synthetic-original" {
			t.Fatalf("rejected mutation changed custody: err=%v", err)
		}
	}
	mutations := []struct {
		name string
		edit func(*controlplane.WalletItem)
	}{
		{"owner", func(item *controlplane.WalletItem) { item.Metadata = map[string]any{"owner_id": "owner-b"} }},
		{"tenant", func(item *controlplane.WalletItem) { item.TenantID = "tenant-b" }},
		{"type", func(item *controlplane.WalletItem) { item.ItemType = "password" }},
		{"type-alias", func(item *controlplane.WalletItem) {
			item.Metadata = map[string]any{"owner_id": "owner-a", "kind": "password"}
		}},
		{"type-invalid", func(item *controlplane.WalletItem) {
			item.Metadata = map[string]any{"owner_id": "owner-a", "item_type": 1}
		}},
		{"owner-missing", func(item *controlplane.WalletItem) { item.Metadata = map[string]any{} }},
		{"owner-conflicting", func(item *controlplane.WalletItem) {
			item.Metadata = map[string]any{"owner_id": "owner-a", "user_id": "owner-b"}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			proposed := original
			mutation.edit(&proposed)
			proposed.Metadata = cloneWalletMetadata(proposed.Metadata)
			proposed.Metadata["secret"] = "synthetic-attacker"
			for _, write := range []func(controlplane.WalletItem) (*controlplane.WalletItem, error){
				func(item controlplane.WalletItem) (*controlplane.WalletItem, error) {
					return store.CreateWalletItem(t.Context(), item)
				},
				func(item controlplane.WalletItem) (*controlplane.WalletItem, error) {
					return store.UpdateWalletItem(t.Context(), item)
				},
				func(item controlplane.WalletItem) (*controlplane.WalletItem, error) {
					return store.UpsertWalletItem(t.Context(), item)
				},
			} {
				if _, err := write(proposed); !errors.Is(err, controlplane.ErrConflict) {
					t.Fatalf("identity mutation admitted: %v", err)
				}
				assertOriginal(t)
			}
		})
	}
	if _, err := store.CreateWalletItem(t.Context(), original); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("duplicate creation admitted: %v", err)
	}
	assertOriginal(t)
	for _, identity := range []struct{ owner, itemType string }{{"owner-b", "ssh_key"}, {"owner-a", "password"}, {"", "ssh_key"}} {
		if err := store.DeleteWalletItemForOwner(t.Context(), original.TenantID, original.ID, identity.owner, identity.itemType); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatalf("unauthorized deletion admitted: %v", err)
		}
		assertOriginal(t)
	}
	for _, write := range []func(controlplane.WalletItem) (*controlplane.WalletItem, error){
		func(item controlplane.WalletItem) (*controlplane.WalletItem, error) {
			return store.UpdateWalletItem(t.Context(), item)
		},
		func(item controlplane.WalletItem) (*controlplane.WalletItem, error) {
			return store.UpsertWalletItem(t.Context(), item)
		},
	} {
		updated := original
		updated.InstanceID = ""
		updated.Metadata = map[string]any{"owner_id": "owner-a", "secret": "synthetic-successor"}
		if _, err := write(updated); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetWalletItem(t.Context(), original.TenantID, original.ID)
		if err != nil || controlplane.WalletOwnerID(got.Metadata) != "owner-a" || got.ItemType != "ssh_key" || got.InstanceID != original.InstanceID || got.Metadata["secret"] != "synthetic-successor" {
			t.Fatalf("legitimate credential refresh failed: err=%v", err)
		}
	}
	mismatchedInstance := original
	mismatchedInstance.InstanceID = "instance-b"
	if _, err := store.UpdateWalletItem(t.Context(), mismatchedInstance); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("update with a different instance id admitted: %v", err)
	}

	legacyCases := []struct {
		name     string
		metadata map[string]any
		owner    string
		allowed  bool
	}{
		{"legacy-owner", map[string]any{"owner_subject_id": "owner-a", "secret": "legacy-canary"}, "owner-a", true},
		{"legacy-ownerless", map[string]any{"secret": "legacy-canary"}, "owner-a", false},
		{"legacy-conflict", map[string]any{"owner_id": "owner-a", "user_id": "owner-b", "secret": "legacy-canary"}, "owner-a", false},
		{"legacy-invalid", map[string]any{"owner_id": 1, "secret": "legacy-canary"}, "1", false},
	}
	if fixtures.seedLegacy != nil {
		for _, legacy := range legacyCases {
			t.Run(legacy.name, func(t *testing.T) {
				stored := controlplane.WalletItem{ID: legacy.name + "-" + suffix, TenantID: original.TenantID, ItemType: "ssh_key", Metadata: legacy.metadata}
				if err := fixtures.seedLegacy(t.Context(), stored); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { assertWalletCleanup(t, store, stored) })
				proposed := stored
				proposed.Metadata = map[string]any{"owner_id": legacy.owner, "secret": "legacy-successor"}
				_, err := store.UpsertWalletItem(t.Context(), proposed)
				if legacy.allowed && err != nil {
					t.Fatal(err)
				}
				if !legacy.allowed && !errors.Is(err, controlplane.ErrConflict) {
					t.Fatalf("legacy custody claimed: %v", err)
				}
				got, err := store.GetWalletItem(t.Context(), stored.TenantID, stored.ID)
				want := "legacy-canary"
				if legacy.allowed {
					want = "legacy-successor"
				}
				if err != nil || got.Metadata["secret"] != want || got.ItemType != stored.ItemType {
					t.Fatalf("legacy custody changed unexpectedly: err=%v", err)
				}
			})
		}
	}
	if err := store.DeleteWalletItem(t.Context(), original.TenantID, original.ID); err != nil {
		return err
	}
	successor := original
	successor.Metadata = map[string]any{"owner_id": "owner-b", "secret": "synthetic-replacement"}
	if _, err := store.CreateWalletItem(t.Context(), successor); err != nil {
		return err
	}
	t.Cleanup(func() { assertWalletCleanup(t, store, successor) })
	if err := store.DeleteWalletItemForOwner(t.Context(), original.TenantID, original.ID, "owner-a", original.ItemType); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("stale delete removed successor custody: %v", err)
	}
	got, err := store.GetWalletItem(t.Context(), successor.TenantID, successor.ID)
	if err != nil || controlplane.WalletOwnerID(got.Metadata) != "owner-b" || got.Metadata["secret"] != "synthetic-replacement" {
		t.Fatalf("successor custody changed: err=%v", err)
	}
	if err := store.DeleteWalletItemForOwner(t.Context(), successor.TenantID, successor.ID, "owner-b", successor.ItemType); err != nil {
		return err
	}
	if _, err := store.UpdateWalletItem(t.Context(), original); err == nil {
		t.Fatal("an update recreated deleted custody")
	}
	if _, err := store.GetWalletItem(t.Context(), original.TenantID, original.ID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("deleted custody became accessible: %v", err)
	}
	return nil
}

func seedLegacyWalletItemWithTenantScope(ctx context.Context, database *sql.DB, schema string, item controlplane.WalletItem) error {
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, TenantGUC, item.TenantID); err != nil {
		return err
	}
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+quotedSchema+`.wallet_items(id, tenant_id, item_type, metadata_json) VALUES ($1,$2,$3,$4::jsonb)`, item.ID, item.TenantID, item.ItemType, string(metadata)); err != nil {
		return err
	}
	return tx.Commit()
}

func seedWalletInstanceWithTenantScope(ctx context.Context, database *DB, schema, tenantID, instanceID string) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, TenantGUC, tenantID); err != nil {
		return err
	}
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+quotedSchema+`.techstack_instances(id, tenant_id, instance_key) VALUES ($1,$2,$3)`, instanceID, tenantID, instanceID); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteWalletInstanceWithTenantScope(ctx context.Context, database *DB, schema, tenantID, instanceID string) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, TenantGUC, tenantID); err != nil {
		return err
	}
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+quotedSchema+`.techstack_instances WHERE id=$1 AND tenant_id=$2`, instanceID, tenantID); err != nil {
		return err
	}
	return tx.Commit()
}

func assertWalletCleanup(t *testing.T, store controlplane.WalletStore, item controlplane.WalletItem) {
	t.Helper()
	if err := store.DeleteWalletItem(context.Background(), item.TenantID, item.ID); err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		t.Errorf("clean Wallet test item: %v", err)
	}
}

func cloneWalletMetadata(metadata map[string]any) map[string]any {
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
