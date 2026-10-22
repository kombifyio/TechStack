// Package testidentity confines test identities to test tenants
// (D-2026-10-22-02). A test identity is a subject the deployment names as one;
// a test tenant is a tenant the server derives for test work. Neither side can
// cross: a test subject never acts in a real tenant, and a real subject never
// acts in a test tenant.
//
// Test tenants are marked by construction, never by a stored flag a user could
// set: the server-owned "test:" namespace, the owner tenant ("usr:<subject>" or
// the naked subject) of a configured test subject, or the configured public
// demo tenant. Tenant ids otherwise come from Auth0 organization claims or the
// Gateway's owner rule, so none of these can be claimed by a real account. The
// public demo identities (pkg/demoguard) are test identities too: they share
// the demo tenant, which holds no customer's data.
package testidentity

import (
	"errors"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/demoguard"
)

const (
	// EnvSubjects is a comma-separated list of test identity subjects (for
	// example "auth0|..."). Subject ids are identifiers, not credentials.
	EnvSubjects = "TECHSTACK_TEST_SUBJECTS"
	// EnvFixtureSubject is the shared fixture subject the release smoke signs
	// in as; it is always a test identity.
	EnvFixtureSubject = "TEST_RIL_PRO_USER_ID"
	// TenantPrefix is the server-owned namespace of test tenants.
	TenantPrefix = "test:"

	ownerTenantPrefix = "usr:"
)

// ErrTenantBoundary is the refusal when a subject and a tenant are on
// different sides of the test boundary.
var ErrTenantBoundary = errors.New("test identities act only in test tenants, and only test identities act in them")

// Subjects returns the configured test subjects.
func Subjects() map[string]struct{} {
	subjects := map[string]struct{}{}
	for _, raw := range append(strings.Split(os.Getenv(EnvSubjects), ","), os.Getenv(EnvFixtureSubject)) {
		if subject := strings.TrimSpace(raw); subject != "" {
			subjects[subject] = struct{}{}
		}
	}
	return subjects
}

// IsTestSubject reports whether subject is a configured test identity: a
// test subject, a public demo subject, or the demo tenant's legacy owner id
// that the identity middleware projects a demo principal onto.
func IsTestSubject(subject string) bool {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return false
	}
	if _, ok := Subjects()[subject]; ok {
		return true
	}
	return demoguard.IsDemoUser(subject) || demoguard.IsDemoTenant(subject)
}

// IsTestTenant reports whether tenantID is a test tenant.
func IsTestTenant(tenantID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return false
	}
	if strings.HasPrefix(tenantID, TenantPrefix) || demoguard.IsDemoTenant(tenantID) {
		return true
	}
	return IsTestSubject(strings.TrimPrefix(tenantID, ownerTenantPrefix))
}

// Admit refuses a subject acting in a tenant on the other side of the test
// boundary. An empty subject or tenant carries no tenant authority and is left
// to the caller's own checks.
func Admit(subject, tenantID string) error {
	subject, tenantID = strings.TrimSpace(subject), strings.TrimSpace(tenantID)
	if subject == "" || tenantID == "" {
		return nil
	}
	if IsTestSubject(subject) != IsTestTenant(tenantID) {
		return ErrTenantBoundary
	}
	return nil
}

// OwnerTenant is the tenant a test identity resolves to at sign-in: its own
// owner tenant, whatever organization claims or memberships it carries.
func OwnerTenant(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	return ownerTenantPrefix + subject
}
