package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestPostgresSCRAMVerifierNeverContainsOrRepeatsRawSecret(t *testing.T) {
	const secret = "runtime-password-that-must-not-enter-ddl"
	first, err := postgresSCRAMVerifier(secret)
	if err != nil {
		t.Fatalf("first verifier: %v", err)
	}
	second, err := postgresSCRAMVerifier(secret)
	if err != nil {
		t.Fatalf("second verifier: %v", err)
	}
	pattern := regexp.MustCompile(`^SCRAM-SHA-256\$4096:[A-Za-z0-9+/]+={0,2}\$[A-Za-z0-9+/]+={0,2}:[A-Za-z0-9+/]+={0,2}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatalf("invalid SCRAM verifier shape")
	}
	if strings.Contains(first, secret) || strings.Contains(second, secret) || first == second {
		t.Fatal("SCRAM verifier exposed the raw secret or reused its random salt")
	}
}

func TestProviderControlBootstrapEnvironmentRequiresBothAuthorities(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv(providerControlRuntimeDatabaseURLEnv, "")
	if providerControlBootstrapEnvironmentConfigured() {
		t.Fatal("empty authorities reported configured")
	}
	t.Setenv("DATABASE_URL", "postgres://migration")
	if providerControlBootstrapEnvironmentConfigured() {
		t.Fatal("migration-only authority reported configured")
	}
	t.Setenv(providerControlRuntimeDatabaseURLEnv, "postgres://runtime")
	if !providerControlBootstrapEnvironmentConfigured() {
		t.Fatal("both explicit authorities were not detected")
	}
}

func TestProviderControlRuntimeGrantAllowlistMatchesBootPosture(t *testing.T) {
	for _, grant := range providerControlRuntimeTableGrants {
		for _, privilege := range strings.Split(grant.privileges, ",") {
			needle := fmt.Sprintf("('%s', '%s')", grant.table, privilege)
			if count := strings.Count(providerControlRuntimeRolePostureQuery, needle); count != 1 {
				t.Errorf("posture capability %s appears %d times", needle, count)
			}
		}
	}
	for _, sequence := range providerControlRuntimeSequences {
		needle := fmt.Sprintf("('%s')", sequence)
		if count := strings.Count(providerControlRuntimeRolePostureQuery, needle); count != 1 {
			t.Errorf("posture sequence %s appears %d times", needle, count)
		}
	}
	for _, function := range providerControlRuntimeFunctions {
		if !strings.Contains(providerControlRuntimeRolePostureQuery, function) {
			t.Errorf("posture query is missing runtime function %q", function)
		}
	}
	if strings.Contains(strings.Join(providerControlRuntimeFunctions, "\n"), "provider_execution_") {
		t.Fatal("runtime function allowlist exposes a privileged ledger trigger")
	}
}

func TestProviderControlRuntimeGrantAllowlistCoversIncidentWorkflowPersistence(t *testing.T) {
	want := map[string]string{
		"provider_incidents": "SELECT,INSERT",
		"ril_workflow_runs":  "SELECT,INSERT",
	}
	for table, privileges := range want {
		found := false
		for _, grant := range providerControlRuntimeTableGrants {
			if grant.table == table && grant.privileges == privileges {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("runtime grant allowlist missing exact %s privileges for %s", privileges, table)
		}
	}
}

func TestProviderControlRuntimeGrantExcludesAdvisoryAndWorkflowMutationAuthority(t *testing.T) {
	for _, grant := range providerControlRuntimeTableGrants {
		if grant.table == "provider_incident_advisory_attempts" ||
			(grant.table == "ril_workflow_runs" && grant.privileges != "SELECT,INSERT") ||
			grant.table == "ril_workflow_steps" || grant.table == "ril_workflow_timers" {
			t.Fatalf("runtime grant retains control-plane authority: %+v", grant)
		}
	}
}

func TestProviderControlRuntimeGrantIncludesCapacityPolicyDigest(t *testing.T) {
	const signature = "managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)"
	if !strings.Contains(strings.Join(providerControlRuntimeFunctions, "\n"), signature) {
		t.Fatalf("runtime function allowlist is missing %q", signature)
	}
	if count := strings.Count(providerControlRuntimeRolePostureQuery, signature); count != 2 {
		t.Fatalf("runtime posture query contains %q %d times, want required+allowlisted checks", signature, count)
	}
}

func TestProviderControlRuntimeGrantAllowlistExcludesAdminAndDirectoryTables(t *testing.T) {
	joined := make([]string, 0, len(providerControlRuntimeTableGrants))
	for _, grant := range providerControlRuntimeTableGrants {
		joined = append(joined, grant.table)
	}
	all := strings.Join(joined, "\n")
	for _, forbidden := range []string{
		"schema_migrations",
		"provider_control_runnable_tenants",
		"provider_decommission_wait_tenants",
		"provider_provision_discovery_observations",
	} {
		if strings.Contains(all, forbidden) {
			t.Errorf("runtime table allowlist contains %q", forbidden)
		}
	}
}

func TestProviderControlRuntimeGrantAllowsResolutionSettlementReadOnly(t *testing.T) {
	var privileges string
	for _, grant := range providerControlRuntimeTableGrants {
		if grant.table == "provider_provision_resolution_decisions" {
			privileges = grant.privileges
			break
		}
	}
	if privileges != "SELECT" {
		t.Fatalf("provider resolution decision rights = %q, want SELECT only", privileges)
	}
}
