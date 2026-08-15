package hooks

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestSanitizeWalletRecordForList(t *testing.T) {
	record := newWalletTestRecord(t)
	record.Set("secret", "super-secret")
	record.Set("totp", "123456")
	record.Set("auto_generated", true)
	record.Set("service_id", "system:admin")

	sanitizeWalletRecordForList(record)

	if got := record.GetString("secret"); got != "" {
		t.Fatalf("expected list secret to be blank, got %q", got)
	}
	if got := record.GetString("totp"); got != "" {
		t.Fatalf("expected list totp to be blank, got %q", got)
	}
	if !record.GetBool("has_secret") {
		t.Fatal("expected list response to report has_secret")
	}
	if !record.GetBool("has_totp") {
		t.Fatal("expected list response to report has_totp")
	}
	if got := record.GetString("item_class"); got != "recovery" {
		t.Fatalf("expected item_class recovery, got %q", got)
	}
	if got := record.GetString("source_type"); got != "system_account" {
		t.Fatalf("expected source_type system_account, got %q", got)
	}
	if got := record.GetString("source_ref"); got != "system:admin" {
		t.Fatalf("expected source_ref system:admin, got %q", got)
	}
	if !record.GetBool("revealable") {
		t.Fatal("expected revealable=true for secret-backed record")
	}
	if got := record.GetString("access_mode"); got != "reveal" {
		t.Fatalf("expected access_mode reveal, got %q", got)
	}
}

func TestSanitizeWalletRecordForDetailView(t *testing.T) {
	record := newWalletTestRecord(t)
	record.Set("secret", "super-secret")
	record.Set("totp", "123456")

	sanitizeWalletRecordForList(record)

	if got := record.GetString("secret"); got != "" {
		t.Fatalf("expected detail secret to be blank, got %q", got)
	}
	if got := record.GetString("totp"); got != "" {
		t.Fatalf("expected detail totp to be blank, got %q", got)
	}
	if !record.GetBool("has_secret") {
		t.Fatal("expected has_secret=true on detail view")
	}
	if !record.GetBool("has_totp") {
		t.Fatal("expected has_totp=true on detail view")
	}
}

func TestApplyWalletDefaultsForToolItem(t *testing.T) {
	record := newWalletTestRecord(t)
	record.Set("name", "PocketBase Admin")
	record.Set("url", "https://techstack.example.com/_/")

	applyWalletDefaults(record)

	if got := record.GetString("item_class"); got != "launch" {
		t.Fatalf("expected item_class launch, got %q", got)
	}
	if got := record.GetString("access_mode"); got != "open" {
		t.Fatalf("expected access_mode open, got %q", got)
	}
	if record.GetBool("revealable") {
		t.Fatal("expected revealable=false for tool item without secret")
	}
}

func TestApplyWalletDefaultsForManagedAccess(t *testing.T) {
	record := newWalletTestRecord(t)
	record.Set("name", "Pocket ID Admin")
	record.Set("username", "admin@kombify.io")
	record.Set("url", "https://id.example.com/admin")

	applyWalletDefaults(record)

	if got := record.GetString("item_class"); got != "user_account" {
		t.Fatalf("expected item_class user_account, got %q", got)
	}
	if got := record.GetString("access_mode"); got != "manage" {
		t.Fatalf("expected access_mode manage, got %q", got)
	}
	if record.GetBool("revealable") {
		t.Fatal("expected revealable=false for access item without secret")
	}
}

func newWalletTestRecord(t *testing.T) *core.Record {
	t.Helper()
	collection := core.NewBaseCollection("wallet")
	collection.Fields.Add(
		&core.TextField{Name: "name"},
		&core.TextField{Name: "username"},
		&core.TextField{Name: "url"},
		&core.TextField{Name: "secret"},
		&core.TextField{Name: "totp"},
		&core.TextField{Name: "service_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "item_class"},
		&core.TextField{Name: "source_type"},
		&core.TextField{Name: "source_ref"},
		&core.TextField{Name: "access_mode"},
		&core.BoolField{Name: "auto_generated"},
		&core.BoolField{Name: "revealable"},
		&core.BoolField{Name: "has_secret"},
		&core.BoolField{Name: "has_totp"},
	)
	return core.NewRecord(collection)
}
