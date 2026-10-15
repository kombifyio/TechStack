package backupstore

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func openNodeBindingStore(t *testing.T) *PostgresCustodyStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping managed backup custody PostgreSQL integration test")
	}
	schema := "node_binding_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	admin, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminDB := stdlib.OpenDB(*admin)
	if _, err := adminDB.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = adminDB.Close()
	})
	config, err := pgx.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })
	for _, statement := range []string{
		`CREATE TABLE stacks (tenant_id text NOT NULL, id text NOT NULL, PRIMARY KEY (tenant_id, id))`,
		`CREATE FUNCTION set_updated_at() RETURNS trigger AS $$ BEGIN NEW.updated_at = now(); RETURN NEW; END $$ LANGUAGE plpgsql`,
		`INSERT INTO stacks VALUES ('tenant-a', 'stack-a')`,
	} {
		if _, err := database.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"052_stack_backup_stores.sql", "053_stack_backup_store_attestation.sql", "142_stack_backup_store_retention.sql", "144_stack_backup_node_binding.sql"} {
		migration, err := os.ReadFile("../db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	store, err := NewPostgresCustodyStore(database, testEncryptor(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(t.Context(), testCredentials()); err != nil {
		t.Fatal(err)
	}
	return store
}

// The node's recipient is pinned at enrollment: the same recipient may repeat,
// a substituted one is refused, and Core compares against the references the
// node issued rather than deriving its own.
func TestPinnedRecipientRefusesSubstitutionAndNodeBindingStaysTheNodes(t *testing.T) {
	store := openNodeBindingStore(t)
	ctx := t.Context()
	pin := RecipientPin{TenantID: "tenant-a", StackID: "stack-a", OwnerKey: "owner-key-1", OwnerKeyID: "owner-1", Fingerprint: "sha256:" + strings.Repeat("a", 64), Generation: 1, SiteRef: "cloud", NodeRef: "cloud-main"}
	if err := store.PinRecipient(ctx, pin); err != nil {
		t.Fatal(err)
	}
	if err := store.PinRecipient(ctx, pin); err != nil {
		t.Fatalf("an identical enrollment was refused: %v", err)
	}
	for name, substituted := range map[string]RecipientPin{
		"another recipient key": func() RecipientPin { p := pin; p.Fingerprint = "sha256:" + strings.Repeat("b", 64); return p }(),
		"another owner key":     func() RecipientPin { p := pin; p.OwnerKey = "owner-key-2"; return p }(),
		"another generation":    func() RecipientPin { p := pin; p.Generation = 2; return p }(),
	} {
		if err := store.PinRecipient(ctx, substituted); !errors.Is(err, ErrRecipientSubstituted) {
			t.Fatalf("%s: err = %v, want substitution refused", name, err)
		}
	}
	if _, err := store.NodeBinding(ctx, "tenant-a", "stack-a"); !errors.Is(err, ErrNodeBindingMissing) {
		t.Fatalf("a binding Core never received was reported: %v", err)
	}
	binding := NodeBinding{BackupTargetRef: "backup-target://sha256/" + strings.Repeat("c", 64), CustodyAttestationRef: "backup-custody-attestation://sha256/" + strings.Repeat("d", 64)}
	if err := store.RecordNodeBinding(ctx, "tenant-a", "stack-a", binding); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNodeBinding(ctx, "tenant-a", "stack-a", binding); err != nil {
		t.Fatalf("the same node binding was refused on repeat: %v", err)
	}
	other := binding
	other.BackupTargetRef = "backup-target://sha256/" + strings.Repeat("e", 64)
	if err := store.RecordNodeBinding(ctx, "tenant-a", "stack-a", other); err == nil {
		t.Fatal("a different node binding replaced the recorded one")
	}
	if recorded, err := store.NodeBinding(ctx, "tenant-a", "stack-a"); err != nil || recorded.BackupTargetRef != binding.BackupTargetRef {
		t.Fatalf("recorded binding = %+v err = %v", recorded, err)
	}
}
