package providercontrol

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// attrValue reads one key from the flat slog attribute slice.
func attrValue(t *testing.T, attrs []any, key string) any {
	t.Helper()
	for index := 0; index+1 < len(attrs); index += 2 {
		if name, _ := attrs[index].(string); name == key {
			return attrs[index+1]
		}
	}
	t.Fatalf("attribute %q is absent from %v", key, attrs)
	return nil
}

// The no-progress branch takes a side-effecting claim, releases it, writes no
// receipt and returns no error, so the caller retries forever. Two production
// decommissions did exactly this for hours on 2026-07-26 while no log query
// over any adapter, provider, or operation term returned a single line.
func TestNoProgressExecutionIsAnnounced(t *testing.T) {
	attrs := adapterExecutionAttrs(
		providerexecutor.Command{
			Operation: providerexecutor.OperationDecommission,
			TenantID:  "auth0|tenant", LeaseID: "lease-abc", OperationID: "op_stalled",
		},
		providerexecutor.Receipt{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted,
		},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted,
		},
		"no_progress", nil,
	)

	for field, want := range map[string]any{
		"operation":     string(providerexecutor.OperationDecommission),
		"operation_id":  "op_stalled",
		"lease_id":      "lease-abc",
		"tenant_id":     "auth0|tenant",
		"phase_before":  string(providerexecutor.PhaseAccepted),
		"phase_after":   string(providerexecutor.PhaseAccepted),
		"result_status": string(providerexecutor.StatusPending),
		"outcome":       "no_progress",
	} {
		if got := attrValue(t, attrs, field); got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
}

// A rejected result is the other silent path: the claim is released and the
// error travels up, but nothing recorded which adapter produced what. The
// underlying text is the part that was being discarded -- a bare reason code
// is what made provider.invalid_spec undiagnosable earlier in this project.
func TestRejectedExecutionCarriesTheReasonAndDetail(t *testing.T) {
	attrs := adapterExecutionAttrs(
		providerexecutor.Command{
			Operation: providerexecutor.OperationDecommission, OperationID: "op_x",
		},
		providerexecutor.Receipt{Phase: providerexecutor.PhaseAccepted},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{
				Code: providerexecutor.ReasonCodeProviderCleanupRequired, Retryable: false,
			},
			Resources: []providerexecutor.ResourceBinding{{BindingID: "datacenter"}},
		},
		"rejected_identity", errors.New("datacenter ref did not resolve"),
	)

	if got := attrValue(t, attrs, "reason_code"); got != providerexecutor.ReasonCodeProviderCleanupRequired {
		t.Errorf("reason_code = %v", got)
	}
	if got := attrValue(t, attrs, "retryable"); got != false {
		t.Errorf("retryable = %v, want false", got)
	}
	if got := attrValue(t, attrs, "resources"); got != 1 {
		t.Errorf("resources = %v, want 1", got)
	}
	if got, _ := attrValue(t, attrs, "detail").(string); got != "datacenter ref did not resolve" {
		t.Errorf("detail = %q, want the underlying error text", got)
	}
}

// A nil reason and a nil detail must not panic or fabricate values; the
// advanced path always passes both as nil.
func TestAdvancedExecutionToleratesAbsentReasonAndDetail(t *testing.T) {
	attrs := adapterExecutionAttrs(
		providerexecutor.Command{Operation: providerexecutor.OperationProvision},
		providerexecutor.Receipt{Phase: providerexecutor.PhaseAccepted},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		},
		"advanced", nil,
	)
	if got := attrValue(t, attrs, "reason_code"); got != "" {
		t.Errorf("reason_code = %v, want empty", got)
	}
	if got := attrValue(t, attrs, "detail"); got != "" {
		t.Errorf("detail = %v, want empty", got)
	}
	if got := attrValue(t, attrs, "phase_after"); got != string(providerexecutor.PhaseResourcesBound) {
		t.Errorf("phase_after = %v", got)
	}
}

// Claim contention returns a nil error and no receipt, so the caller retries.
// If the claim never clears, that is the same forever-retry as a no-progress
// result and must be equally visible.
func TestClaimContentionIsAnnouncedWithTheHeadItObserved(t *testing.T) {
	attrs := adapterExecutionAttrs(
		providerexecutor.Command{
			Operation: providerexecutor.OperationDecommission, OperationID: "op_contended",
		},
		providerexecutor.Receipt{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted,
		},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted,
		},
		"claim_contended", ErrExecutionClaimHeld,
	)
	if got := attrValue(t, attrs, "outcome"); got != "claim_contended" {
		t.Errorf("outcome = %v", got)
	}
	if got, _ := attrValue(t, attrs, "detail").(string); got != ErrExecutionClaimHeld.Error() {
		t.Errorf("detail = %q, want the claim error", got)
	}
}
