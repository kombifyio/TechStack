package db

import "testing"

// Managed Postgres keeps PUBLIC CONNECT/TEMPORARY on provider databases a
// tenant role cannot revoke; only the role's own authority there is excess.
func TestApplicationOtherDatabaseAuthorityIgnoresOnlyPublicInheritance(t *testing.T) {
	providerPublic := []otherDatabaseGrant{{PublicGrantee: true}, {PublicGrantee: true}}
	if otherDatabaseAuthorityExcess(providerPublic) {
		t.Fatal("PUBLIC-inherited CONNECT/TEMPORARY on a provider database was rejected")
	}
	for name, grant := range map[string]otherDatabaseGrant{
		"explicit grant":   {RoleGrantee: true},
		"membership grant": {RoleGrantee: true},
		"owned database":   {OwnedByRole: true, PublicGrantee: true},
	} {
		if !otherDatabaseAuthorityExcess(append(providerPublic, grant)) {
			t.Fatalf("%s on another database was admitted", name)
		}
	}
}
