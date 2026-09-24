package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/systemwallet"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestConfirmSystemAccountReset(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		confirmation string
		wantErr      bool
	}{
		{name: "exact role", role: "admin", confirmation: "admin"},
		{name: "case insensitive", role: "developer", confirmation: "Developer"},
		{name: "missing confirmation", role: "admin", confirmation: "", wantErr: true},
		{name: "wrong role", role: "admin", confirmation: "developer", wantErr: true},
		{name: "missing role", role: "", confirmation: "admin", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confirmSystemAccountReset(tt.role, tt.confirmation)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected reset confirmation rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSystemAccountConfigForRole(t *testing.T) {
	t.Setenv("TECHSTACK_ADMIN_EMAIL", "ops@example.test")
	t.Setenv("TECHSTACK_DEVELOPER_EMAIL", "dev@example.test")
	t.Setenv("TECHSTACK_SUPERUSER_EMAIL", "root@example.test")

	tests := []struct {
		name            string
		role            string
		email           string
		collection      string
		authorizedEmail string
		walletName      string
	}{
		{"superuser", " superuser ", "root@example.test", "_superusers", "ops@example.test", "PocketBase Superuser"},
		{"admin", "admin", "ops@example.test", "users", "ops@example.test", "kombifyTechstack Admin"},
		{"developer", "developer", "dev@example.test", "users", "dev@example.test", "kombifyTechstack Developer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, ok := systemAccountConfigForRole(tt.role)
			if !ok {
				t.Fatalf("expected role %q to be accepted", tt.role)
			}
			if cfg.email != tt.email || cfg.collection != tt.collection || cfg.authorizedEmail != tt.authorizedEmail || cfg.walletName != tt.walletName {
				t.Fatalf("unexpected config: %+v", cfg)
			}
			if !cfg.allowsReset(strings.ToUpper(tt.authorizedEmail)) {
				t.Fatalf("expected configured account to authorize reset")
			}
			if cfg.allowsReset("other@example.test") {
				t.Fatalf("unexpected reset authorization for unrelated requester")
			}
		})
	}
}

func TestSystemAccountConfigForRoleDefaultsAndInvalidRole(t *testing.T) {
	cfg, ok := systemAccountConfigForRole("admin")
	if !ok {
		t.Fatalf("expected admin role to be accepted")
	}
	if cfg.email != "admin@techstack.local" || cfg.authorizedEmail != "admin@techstack.local" {
		t.Fatalf("unexpected admin defaults: %+v", cfg)
	}

	if _, ok := systemAccountConfigForRole("owner"); ok {
		t.Fatalf("unexpected invalid role acceptance")
	}
}

func TestSystemAccountResetRejectsSignedEdgeIdentityWithoutLocalSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system-accounts/admin/reset", strings.NewReader(`{"confirm":"admin"}`))
	req.SetPathValue("role", "admin")
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "api-key:techstack-admin"}))

	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}

	err := (systemAccountRouteHandlers{}).reset(event)
	if err != nil {
		t.Fatalf("reset returned unexpected router error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "local owner session") {
		t.Fatalf("response did not explain local session requirement: %s", rec.Body.String())
	}
}

func TestSystemAccountRecoveryUsesCanonicalWalletIdentity(t *testing.T) {
	store := controlplane.NewMemoryStore()
	cfg, _ := systemAccountConfigForRole(systemAccountRoleAdmin)
	if err := systemwallet.UpsertCredential(context.Background(), store, "tenant-1", "owner-1", systemwallet.Credential{
		Role: cfg.role, Name: cfg.walletName, Username: cfg.email, Secret: "recovery-secret",
	}); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}
	item, err := store.GetWalletItem(context.Background(), "tenant-1", systemwallet.ItemID("tenant-1", "owner-1", systemAccountRoleAdmin))
	if err != nil || item.Metadata["owner_id"] != "owner-1" || item.Metadata["revealable"] != true {
		t.Fatalf("canonical wallet item = %#v, err=%v", item, err)
	}
	secret, _ := item.Metadata["secret"].(string)
	if secret == "" || secret == "recovery-secret" || !auth.IsEncrypted(secret) {
		t.Fatalf("recovery credential must be stored as an encrypted envelope, got %q", secret)
	}
}
