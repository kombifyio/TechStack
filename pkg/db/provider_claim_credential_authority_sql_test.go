package db

import (
	"strings"
	"testing"
)

func TestProviderClaimCredentialAuthorityMigrationIsFailClosed(t *testing.T) {
	content := readDBFile(t, "migrations/032_provider_claim_credential_authority.sql")
	required := []string{
		"add column if not exists custody_hash text",
		"add column if not exists connection_hash text",
		"add column if not exists valid_from timestamptz",
		"add column if not exists valid_until timestamptz",
		"new credential handle requires immutable claim authority",
		"add column if not exists claim_access text",
		"claim_access in ('read_only', 'side_effecting')",
		"provider_expected_execution_claim_access",
		"operation_name in ('plan', 'observe')",
		"receipt_phase = 'resources_bound'",
		"operation_name = 'reconcile'",
		"receipt_phase in ('delete_accepted', 'absence_pending')",
		"new.claimed_at is distinct from old.claimed_at",
		"old.lease_expires_at <= db_at",
		"expired provider execution claim cannot be heartbeat-renewed",
		"requested_ttl := new.lease_expires_at - new.claimed_at",
		"tg_op = 'insert' or (tg_op = 'update' and new_authorization)",
		"requested_ttl is null",
		"requested_ttl > interval '15 minutes'",
		"new.claimed_at := db_at",
		"new.lease_expires_at := db_at + requested_ttl",
		"new.lease_expires_at > db_at + interval '15 minutes'",
		"provider execution claim heartbeat lease is outside the allowed range",
		"locked_custody_hash is distinct from command_custody_hash",
		"locked_connection_hash is distinct from command_connection_hash",
		"locked_revoked_at is not null",
		"locked_valid_from > db_at",
		"locked_valid_until <= db_at",
		"for share of credential",
		"db_at := clock_timestamp()",
		"runtime_lease.cancelled_at",
		"runtime_server.decommissioned_at",
		"for share of runtime_lease, runtime_server",
		"provider execution claim is fenced by cancellation or teardown intent",
		"provider decommission claim has no teardown intent",
		"terminal server decommission tombstone",
	}
	lower := strings.ToLower(content)
	for _, needle := range required {
		if !strings.Contains(lower, needle) {
			t.Errorf("provider claim credential migration missing %q", needle)
		}
	}
}

func TestProviderClaimCredentialAuthorityDoesNotReauthorizeHeartbeatOrAppend(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/032_provider_claim_credential_authority.sql"))
	if !strings.Contains(content, "tg_op = 'insert' or") ||
		!strings.Contains(content, "new.claim_token_digest is distinct from old.claim_token_digest") ||
		!strings.Contains(content, "old.state is distinct from 'active'") {
		t.Fatal("credential guard does not distinguish a new authorization from same-token heartbeat renewal")
	}
	for _, forbidden := range []string{
		"create trigger provider_operation_receipts",
		"create trigger provider_operations_credential",
		"before insert on provider_operation_receipts",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("credential migration may not block post-call append via %q", forbidden)
		}
	}
}

func TestProviderClaimCredentialAuthorityReadsDBTimeAfterCredentialLock(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/032_provider_claim_credential_authority.sql"))
	lockIndex := strings.Index(content, "for share of credential;")
	timeIndex := strings.Index(content, "db_at := clock_timestamp()")
	if lockIndex < 0 || timeIndex < 0 || timeIndex <= lockIndex {
		t.Fatal("credential authority must capture DB time only after the exact handle row is locked")
	}
}

func TestProviderClaimCredentialAuthoritySeparatesFirstDispatchFromOperatorAdoption(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/032_provider_claim_credential_authority.sql"))
	for _, required := range []string{
		"amo_resources_bound_transition",
		"first_dispatch_append",
		"claim.state = 'consumed'",
		"app.provider_execution_claim_token",
		"operator_adoption := amo_resources_bound_transition and not first_dispatch_append",
		"app.provider_resolution_token",
		"amo exact-candidate adoption requires its transaction-bound decision",
		"amo provision accepted transition requires its consumed first-claim guard",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider operation authority correction missing %q", required)
		}
	}
}
