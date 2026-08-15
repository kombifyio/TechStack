package rilactionexecution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestPinnedCLIDispatcherProducesClosedRedactedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	request := testRequest(now)
	request.Primitive = rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: request.Primitive.ContractHash, OperationClass: "verification"}
	request.Target = rilaction.TargetBinding{Scope: rilaction.TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "agent-1", ExecutionChannelRef: "host-channel-node-1"}
	dispatcher := &PinnedCLIDispatcher{now: func() time.Time { return now }}

	for _, succeeded := range []bool{true, false} {
		evidence, err := dispatcher.evidence(request, succeeded)
		if err != nil {
			t.Fatalf("evidence(%t): %v", succeeded, err)
		}
		if validationErr := rilaction.ValidateEvidenceForRequest(request, evidence); validationErr != nil {
			t.Fatalf("validate(%t): %v", succeeded, validationErr)
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"provider", "credential", "secret", "stderr", "command_result", "raw_log"} {
			if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
				t.Fatalf("evidence exposed %q: %s", forbidden, encoded)
			}
		}
		if !succeeded && !strings.HasPrefix(evidence.ProtectedDiagnosticRef, "diagnostic:") {
			t.Fatalf("failed evidence lacks protected diagnostic ref: %#v", evidence)
		}
	}
}
