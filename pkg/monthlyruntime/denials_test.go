package monthlyruntime

import (
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
