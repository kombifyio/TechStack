package openapi

import (
	"strings"
	"testing"
)

func TestPublicSpecExcludesInternalVMLeaseRoutes(t *testing.T) {
	if strings.Contains(string(Spec), "/api/v1/internal/vm-leases") {
		t.Fatal("public OpenAPI spec must not expose internal VM lease routes")
	}
}

func TestPublicSpecExcludesServicecallAuthScheme(t *testing.T) {
	raw := string(Spec)
	if strings.Contains(raw, "servicecallAuth") || strings.Contains(raw, "X-Kombify-Service-Auth") {
		t.Fatal("public OpenAPI spec must not expose internal servicecall auth")
	}
}
