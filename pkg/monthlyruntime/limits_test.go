package monthlyruntime

import (
	"strings"
	"testing"
)

func TestManagedRuntimeCapacityReservationDenialDetailsUsesTruthfulCustodyShape(t *testing.T) {
	details := ManagedRuntimeCapacityReservationDenialDetails(ProviderIONOS, 3, 4)
	if got := details["error_code"]; got != ManagedRuntimeMaxServersErrorCode {
		t.Fatalf("error_code = %v, want %s", got, ManagedRuntimeMaxServersErrorCode)
	}
	if got := details["retryable"]; got != false {
		t.Fatalf("retryable = %v, want false", got)
	}
	if got := details["reserved_servers"]; got != 4 {
		t.Fatalf("reserved_servers = %v, want 4", got)
	}
	if _, exists := details["active_servers"]; exists {
		t.Fatalf("capacity custody must not be presented as active servers: %#v", details)
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok || !strings.Contains(guidance["body"].(string), "conservatively retains every reservation") {
		t.Fatalf("user_guidance = %#v, want truthful reservation wording", details["user_guidance"])
	}
	steps, ok := guidance["next_steps"].([]string)
	if !ok {
		t.Fatalf("next_steps = %#v, want string list", guidance["next_steps"])
	}
	joined := strings.Join(steps, " ")
	if strings.Contains(joined, "Wait for") || strings.Contains(joined, "Decommission an existing") ||
		!strings.Contains(joined, "operator reconciliation") {
		t.Fatalf("next_steps = %#v, want support reconciliation without a false automatic release promise", steps)
	}
}

func TestDecommissionProtectedDetailsShape(t *testing.T) {
	details := DecommissionProtectedDetails(ProviderCentron, "lease-main")
	if got := details["error_code"]; got != DecommissionBlockedProtectedErrorCode {
		t.Fatalf("error_code = %v, want %s", got, DecommissionBlockedProtectedErrorCode)
	}
	if got := details["reason_code"]; got != ReasonLeaseProtected {
		t.Fatalf("reason_code = %v, want %s", got, ReasonLeaseProtected)
	}
	if got := details["force_offered"]; got != false {
		t.Fatalf("force_offered = %v, want false", got)
	}
	if got := details["lease_id"]; got != "lease-main" {
		t.Fatalf("lease_id = %v, want lease-main", got)
	}
}
