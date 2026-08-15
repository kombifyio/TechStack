package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestConfirmSystemAccountReset(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		confirmation string
		wantErr      string
	}{
		{name: "exact role", role: "admin", confirmation: "admin"},
		{name: "case insensitive", role: "developer", confirmation: "Developer"},
		{name: "missing confirmation", role: "admin", confirmation: "", wantErr: "must match"},
		{name: "wrong role", role: "admin", confirmation: "developer", wantErr: "must match"},
		{name: "missing role", role: "", confirmation: "admin", wantErr: "role is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confirmSystemAccountReset(tt.role, tt.confirmation)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
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
