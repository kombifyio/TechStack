package identity

import (
	"context"
	"testing"
)

func TestFromHeaders(t *testing.T) {
	headers := map[string]string{
		"X-User-ID":    "usr_abc123",
		"X-Org-ID":     "org_xyz",
		"X-User-Email": "test@kombify.io",
		"X-User-Roles": "admin, member",
	}
	id := FromHeaders(func(key string) string { return headers[key] })

	if id.UserID != "usr_abc123" {
		t.Errorf("UserID = %q, want %q", id.UserID, "usr_abc123")
	}
	if id.OrgID != "org_xyz" {
		t.Errorf("OrgID = %q, want %q", id.OrgID, "org_xyz")
	}
	if id.Email != "test@kombify.io" {
		t.Errorf("Email = %q, want %q", id.Email, "test@kombify.io")
	}
	if len(id.Roles) != 2 || id.Roles[0] != "admin" || id.Roles[1] != "member" {
		t.Errorf("Roles = %v, want [admin member]", id.Roles)
	}
	if !id.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true")
	}
	if !id.HasRole("admin") {
		t.Error("expected HasRole(admin) == true")
	}
	if id.HasRole("superadmin") {
		t.Error("expected HasRole(superadmin) == false")
	}
}

func TestFromHeaders_Empty(t *testing.T) {
	id := FromHeaders(func(string) string { return "" })
	if id.IsAuthenticated() {
		t.Error("expected empty headers → not authenticated")
	}
	if id.HasRole("any") {
		t.Error("expected no roles")
	}
}

func TestContextRoundtrip(t *testing.T) {
	id := &Identity{UserID: "u1", OrgID: "o1"}
	ctx := NewContext(context.Background(), id)
	got := FromContext(ctx)
	if got == nil || got.UserID != "u1" || got.OrgID != "o1" {
		t.Errorf("round-trip failed: got %+v", got)
	}
}

func TestFromContext_Nil(t *testing.T) {
	got := FromContext(context.Background())
	if got != nil {
		t.Errorf("expected nil from empty context, got %+v", got)
	}
	got = FromContext(nil)
	if got != nil {
		t.Errorf("expected nil from nil context, got %+v", got)
	}
}
