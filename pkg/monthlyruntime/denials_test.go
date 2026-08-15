package monthlyruntime

import (
	"errors"
	"testing"
)

// assertActionableEnvelope checks the shared structured-error envelope contract:
// stable error_code/reason_code, retryable flag, and actionable user_guidance.
func assertActionableEnvelope(t *testing.T, d map[string]any, wantErrorCode, wantReasonCode string, wantRetryable bool) {
	t.Helper()
	if got := d["error_code"]; got != wantErrorCode {
		t.Errorf("error_code = %v, want %q", got, wantErrorCode)
	}
	if got := d["reason_code"]; got != wantReasonCode {
		t.Errorf("reason_code = %v, want %q", got, wantReasonCode)
	}
	if got := d["retryable"]; got != wantRetryable {
		t.Errorf("retryable = %v, want %v", got, wantRetryable)
	}
	if d["capability"] != ManagedRuntimeCapability {
		t.Errorf("capability = %v, want %v", d["capability"], ManagedRuntimeCapability)
	}
	guidance, ok := d["user_guidance"].(map[string]any)
	if !ok {
		t.Fatalf("user_guidance is not a map: %T", d["user_guidance"])
	}
	if title, _ := guidance["title"].(string); title == "" {
		t.Error("user_guidance.title is empty")
	}
	if body, _ := guidance["body"].(string); body == "" {
		t.Error("user_guidance.body is empty")
	}
	steps, ok := guidance["next_steps"].([]string)
	if !ok || len(steps) == 0 {
		t.Errorf("user_guidance.next_steps missing/empty: %v", guidance["next_steps"])
	}
}

func TestManagedRuntimeProvisionFailureDetailsAreActionable(t *testing.T) {
	d := ManagedRuntimeProvisionFailureDetails("ionos-managed", "job-123", "", errors.New("boom"))
	assertActionableEnvelope(t, d, ManagedRuntimeProvisionFailedErrorCode, ReasonProvisionRequestFailed, true)
	if d["job_id"] != "job-123" {
		t.Errorf("job_id = %v, want job-123", d["job_id"])
	}
	if d["error_details"] != "boom" {
		t.Errorf("error_details = %v, want boom", d["error_details"])
	}
	if d["provider_id"] != "ionos-managed" {
		t.Errorf("provider_id = %v, want ionos-managed", d["provider_id"])
	}

	// A caller-supplied reason_code overrides the default; nil err omits details.
	d2 := ManagedRuntimeProvisionFailureDetails("", "", "kombify_me_registration_failed", nil)
	if d2["reason_code"] != "kombify_me_registration_failed" {
		t.Errorf("reason_code = %v, want kombify_me_registration_failed", d2["reason_code"])
	}
	if _, ok := d2["job_id"]; ok {
		t.Error("job_id should be omitted when empty")
	}
	if _, ok := d2["error_details"]; ok {
		t.Error("error_details should be omitted when err is nil")
	}
	if d2["provider_id"] != ProviderCentron {
		t.Errorf("provider_id default = %v, want %s", d2["provider_id"], ProviderCentron)
	}
}

func TestStalledEnrollmentDetailsAreActionable(t *testing.T) {
	d := StalledEnrollmentDetails("centron-managed", "lease-abc")
	assertActionableEnvelope(t, d, RuntimeEnrollmentStalledErrorCode, ReasonEnrollmentStalled, true)
	if d["lease_id"] != "lease-abc" {
		t.Errorf("lease_id = %v, want lease-abc", d["lease_id"])
	}
}

func TestDecommissionUnreachableDetailsOfferForce(t *testing.T) {
	withForce := DecommissionUnreachableDetails("ionos-managed", "lease-xyz", true)
	assertActionableEnvelope(t, withForce, DecommissionBlockedUnreachableErrorCode, ReasonRuntimeUnreachable, true)
	if withForce["force_offered"] != true {
		t.Errorf("force_offered = %v, want true", withForce["force_offered"])
	}
	guidance := withForce["user_guidance"].(map[string]any)
	if steps := guidance["next_steps"].([]string); len(steps) < 2 {
		t.Errorf("force-offered guidance should include a force step, got %v", steps)
	}

	noForce := DecommissionUnreachableDetails("ionos-managed", "lease-xyz", false)
	if noForce["force_offered"] != false {
		t.Errorf("force_offered = %v, want false", noForce["force_offered"])
	}
	guidance = noForce["user_guidance"].(map[string]any)
	if steps := guidance["next_steps"].([]string); len(steps) != 1 {
		t.Errorf("no-force guidance should not include a force step, got %v", steps)
	}
	if got := guidance["body"]; got != "The managed runtime did not respond to the decommission request. Forced decommission is unavailable until durable provider reconciliation is ready." {
		t.Errorf("no-force guidance body = %v, want an honest unavailable-capability message", got)
	}
}

// The operational envelopes must share the entitlement builder's key set so a
// single frontend/OpenAPI shape parses all of them.
func TestOperationalEnvelopesShareEntitlementKeySet(t *testing.T) {
	base := ManagedRuntimeEntitlementDenialDetails("centron-managed", "", nil, nil)
	for _, d := range []map[string]any{
		ManagedRuntimeProvisionFailureDetails("centron-managed", "job-1", "", nil),
		StalledEnrollmentDetails("centron-managed", "lease-1"),
		DecommissionUnreachableDetails("centron-managed", "lease-1", true),
	} {
		for _, key := range []string{"phase", "phase_label", "error_code", "reason_code", "capability", "provider_id", "required_features", "missing_features", "retryable", "user_guidance", "support_context"} {
			if _, ok := d[key]; !ok {
				t.Errorf("envelope missing shared key %q (present in entitlement envelope: %v)", key, base[key] != nil)
			}
		}
	}
}
