package authmw

import (
	"context"
	"testing"

	"github.com/kombifyio/go-common/authsession"
)

func TestNewUsesTechStackCookieName(t *testing.T) {
	middleware := New(nil)

	if middleware.CookieName() != DefaultSessionCookieName {
		t.Fatalf("CookieName = %q, want %q", middleware.CookieName(), DefaultSessionCookieName)
	}
}

func TestContextHelpersAreReexported(t *testing.T) {
	claims := &authsession.Claims{Subject: "user-1", TenantID: "tenant-1"}
	ctx := WithClaims(context.Background(), claims)

	got, err := ClaimsFrom(ctx)
	if err != nil {
		t.Fatalf("ClaimsFrom returned error: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Fatalf("Subject = %q, want %q", got.Subject, claims.Subject)
	}

	tenant, err := TenantFrom(ctx)
	if err != nil {
		t.Fatalf("TenantFrom returned error: %v", err)
	}
	if tenant != claims.TenantID {
		t.Fatalf("Tenant = %q, want %q", tenant, claims.TenantID)
	}
}
