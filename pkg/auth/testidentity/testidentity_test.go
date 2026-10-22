package testidentity

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/demoguard"
)

func TestAdmitKeepsTestIdentitiesAndRealTenantsApart(t *testing.T) {
	t.Setenv(EnvSubjects, " auth0|tester , auth0|second")
	t.Setenv(EnvFixtureSubject, "auth0|fixture")

	for _, tc := range []struct {
		name, subject, tenant string
		refused               bool
	}{
		{"test subject in its owner tenant", "auth0|tester", "usr:auth0|tester", false},
		{"test subject in its naked owner tenant", "auth0|tester", "auth0|tester", false},
		{"test subject in a server test tenant", "auth0|tester", "test:journey", false},
		{"fixture subject in its owner tenant", "auth0|fixture", "usr:auth0|fixture", false},
		{"test subject in another test identity's tenant", "auth0|tester", "usr:auth0|second", false},
		{"test subject in a real organization", "auth0|tester", "org_real", true},
		{"test subject in a real owner tenant", "auth0|tester", "usr:auth0|real", true},
		{"real subject in a test owner tenant", "auth0|real", "usr:auth0|tester", true},
		{"real subject in a server test tenant", "auth0|real", "test:journey", true},
		{"real subject in its own tenant", "auth0|real", "usr:auth0|real", false},
		{"real subject in an organization", "auth0|real", "org_real", false},
		{"no tenant carries no authority", "auth0|tester", "", false},
	} {
		err := Admit(tc.subject, tc.tenant)
		if tc.refused != errors.Is(err, ErrTenantBoundary) {
			t.Fatalf("%s: Admit(%q, %q) = %v, refused want %v", tc.name, tc.subject, tc.tenant, err, tc.refused)
		}
	}
}

func TestPublicDemoIdentitiesStayInTheDemoTenant(t *testing.T) {
	t.Setenv(EnvSubjects, "")
	t.Setenv(EnvFixtureSubject, "")
	t.Setenv(demoguard.EnvDemoUserIDs, "oauth2|kombify-demo|visitor")
	t.Setenv(demoguard.EnvDemoTenantID, "tenant-demo")

	for _, tc := range []struct {
		name, subject, tenant string
		refused               bool
	}{
		{"demo principal in the demo tenant", "oauth2|kombify-demo|visitor", "tenant-demo", false},
		{"projected legacy demo owner in the demo tenant", "tenant-demo", "tenant-demo", false},
		{"demo principal in a real organization", "oauth2|kombify-demo|visitor", "org_real", true},
		{"real subject in the demo tenant", "auth0|real", "tenant-demo", true},
	} {
		err := Admit(tc.subject, tc.tenant)
		if tc.refused != errors.Is(err, ErrTenantBoundary) {
			t.Fatalf("%s: Admit(%q, %q) = %v, refused want %v", tc.name, tc.subject, tc.tenant, err, tc.refused)
		}
	}
}

func TestNoConfiguredSubjectsStillGuardsTheTestNamespace(t *testing.T) {
	t.Setenv(EnvSubjects, "")
	t.Setenv(EnvFixtureSubject, "")
	if err := Admit("auth0|real", "test:journey"); !errors.Is(err, ErrTenantBoundary) {
		t.Fatalf("a real subject entered the server test namespace: %v", err)
	}
	if err := Admit("auth0|real", "usr:auth0|real"); err != nil {
		t.Fatalf("a real subject was refused its own tenant: %v", err)
	}
}

func TestOwnerTenant(t *testing.T) {
	if got := OwnerTenant(" auth0|tester "); got != "usr:auth0|tester" {
		t.Fatalf("OwnerTenant = %q", got)
	}
	if OwnerTenant(" ") != "" {
		t.Fatal("an empty subject has no owner tenant")
	}
}
