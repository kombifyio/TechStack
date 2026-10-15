package main

import (
	"github.com/kombifyio/techstack/pkg/db"
	"testing"
)

// Missing signing login/rows cannot turn an existing-installation release into
// implicit key genesis. The normal producer carries genesis=false.
func TestExistingInstallationPreparationRefusesImplicitIssuerGenesis(t *testing.T) {
	for _, mode := range []string{"", "verify", "prepare"} {
		t.Setenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE", mode)
		t.Setenv(db.EnvAllowDBGenesis, "false")
		if installationIssuerGenesisAuthorized() {
			t.Fatal("existing-installation preparation admitted key issuance")
		}
	}
	t.Setenv(db.EnvAllowDBGenesis, "true")
	t.Setenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE", "verify")
	if installationIssuerGenesisAuthorized() {
		t.Fatal("verification admitted key issuance")
	}
}
