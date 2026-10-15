package stacks

import (
	"errors"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/activities"
)

// This probes the receipt comparison after canonical request hashing. The
// opaque digest/identity DTOs provide no approval, Grant, database or node.
func TestGovernedOperationReceiptUsesCanonicalPersistedDigest(t *testing.T) {
	input := activities.OperationInput{TenantID: "tenant-model", OwnerSubjectID: "subject-model"}
	admitted := actions.BeginResult{
		Request:   rilaction.Request{ExecutionID: "execution-model", Target: rilaction.TargetBinding{RuntimeInstanceRef: "agent-model"}},
		Admission: actions.ExecutionAdmission{Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	requestDigest := "sha256:" + strings.Repeat("a", 64)
	receipt := map[string]any{
		"card_id": "card-model", "execution_id": admitted.Request.ExecutionID,
		"tenant_id": input.TenantID, "owner_id": input.OwnerSubjectID,
		"agent_id": admitted.Request.Target.RuntimeInstanceRef, "operation": "verify",
		"request_digest": requestDigest, "admission_digest": admitted.Admission.Digest,
	}
	for _, loadedDigest := range []string{"", "sha256:" + strings.Repeat("c", 64)} {
		admitted.Admission.RequestDigest = loadedDigest
		if err := validateGovernedOperationReceipt(input, "card-model", "verify", admitted, requestDigest, receipt); err != nil {
			t.Fatalf("canonical request binding was lost on resumed admission: %v", err)
		}
	}
	if err := validateGovernedOperationReceipt(input, "card-model", "verify", admitted, admitted.Admission.RequestDigest, receipt); !errors.Is(err, actions.ErrExecutionAdmission) {
		t.Fatalf("foreign persisted request digest was admitted: %v", err)
	}
	if err := validateGovernedOperationReceipt(input, "card-model", "verify", admitted, "", receipt); !errors.Is(err, actions.ErrExecutionAdmission) {
		t.Fatalf("missing canonical request digest was admitted: %v", err)
	}
}
