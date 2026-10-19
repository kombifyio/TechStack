package guardposture

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// approve registers an owner approval of automatic repair and returns it with
// its registered policy ID and version.
func approve(t *testing.T, svc *Service, controls []string, expires time.Time) *AutoRepairPolicy {
	t.Helper()
	policy, err := svc.SetAutoRepair(context.Background(), ident.TenantID, ident.WorkerID, "auth0|owner", true, controls, expires)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func localEvidence(t *testing.T, policy *AutoRepairPolicy, operationID string, started time.Time, mutate func(*LocalOperationEvidence)) LocalOperationEvidence {
	t.Helper()
	e := LocalOperationEvidence{
		OperationID: operationID, TenantID: ident.TenantID, WorkerID: ident.WorkerID, Generation: 2,
		PolicyID: policy.PolicyID, PolicyVersion: policy.Version, PolicyExpiresAt: *policy.ExpiresAt,
		Action: LocalActionHostSecurityRepair, Controls: []string{ControlFirewall}, StartedAt: started,
		Report:  guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportTerminal},
		Outcome: "applied", Steps: []RepairStep{{Control: ControlFirewall, Status: "applied"}},
	}
	if mutate != nil {
		mutate(&e)
	}
	digest, err := e.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	e.Report.Digest = digest
	return e
}

// Evidence is accepted only against registered, current authority for the
// exact scope it names: an unregistered or withdrawn version, an operation
// started after the version ended, a control or action the version does not
// cover, a foreign tenant or a generation newer than the authenticating
// credential are refused, and none of them leaves a record.
func TestLocalOperationEvidenceWithoutRegisteredAuthorityIsRejected(t *testing.T) {
	svc, _, _, clk := newTestService(t)
	ctx := context.Background()
	policy := approve(t, svc, []string{ControlFirewall}, t0.Add(2*time.Hour))
	clk.now = t0.Add(30 * time.Minute)

	cases := []struct {
		name     string
		evidence func() LocalOperationEvidence
		code     string
	}{
		{"unregistered version", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-1", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) { e.PolicyVersion = policy.Version + 7 })
		}, DenyLocalPolicyUnregistered},
		{"self-extended expiry", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-2", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) { e.PolicyExpiresAt = e.PolicyExpiresAt.Add(24 * time.Hour) })
		}, guardbinding.CodeBindingMismatch},
		{"control outside the policy", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-3", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) {
				e.Controls, e.Steps = []string{ControlSysctl}, []RepairStep{{Control: ControlSysctl, Status: "applied"}}
			})
		}, DenyLocalPolicyScope},
		{"arbitrary action", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-4", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) { e.Action = "shell" })
		}, DenyLocalPolicyScope},
		{"another tenant", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-5", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) { e.TenantID = "tenant-2" })
		}, guardbinding.CodeBindingMismatch},
		{"generation newer than the credential", func() LocalOperationEvidence {
			return localEvidence(t, policy, "op-6", t0.Add(10*time.Minute), func(e *LocalOperationEvidence) { e.Generation = 9 })
		}, guardbinding.CodeStaleGeneration},
	}
	for _, tc := range cases {
		_, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, tc.evidence())
		if denial, ok := err.(*guardbinding.Denial); !ok || denial.Code != tc.code {
			t.Errorf("%s: got %v, want denial %s", tc.name, err, tc.code)
		}
	}

	// The owner withdraws the approval: an operation started afterwards under
	// the old version is refused, even once the Guard reconnects.
	clk.now = t0.Add(40 * time.Minute)
	if _, err := svc.SetAutoRepair(ctx, ident.TenantID, ident.WorkerID, "auth0|owner", false, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	clk.now = t0.Add(3 * time.Hour)
	_, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, localEvidence(t, policy, "op-7", t0.Add(50*time.Minute), nil))
	if denial, ok := err.(*guardbinding.Denial); !ok || denial.Code != guardbinding.CodeAuthorityExpired {
		t.Errorf("operation after withdrawal: got %v", err)
	}
	if repairs, _ := svc.Repairs(ctx, ident.TenantID, ident.WorkerID, 0); len(repairs) != 0 {
		t.Fatalf("rejected evidence left records: %+v", repairs)
	}
}

// A committed revision is acknowledged again when repeated, a different record
// for the same revision is refused, and an outcome_unknown is settled only by a
// later revision that names the evidence it supersedes.
func TestLocalOperationEvidenceIsAcknowledgedIdempotently(t *testing.T) {
	svc, _, _, clk := newTestService(t)
	ctx := context.Background()
	policy := approve(t, svc, []string{ControlFirewall, ControlBruteForce}, t0.Add(2*time.Hour))
	clk.now = t0.Add(20 * time.Minute)
	started := t0.Add(10 * time.Minute)

	unknown := localEvidence(t, policy, "op-1", started, func(e *LocalOperationEvidence) {
		e.Report, e.Outcome, e.Steps = guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: "executor lost"}, "", nil
	})
	first, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, unknown)
	if err != nil || !first.Acknowledged || first.Duplicate {
		t.Fatalf("first revision: %+v %v", first, err)
	}
	again, err := svc.AcceptLocalOperation(ctx, ident, "node", 3, unknown)
	if err != nil || !again.Acknowledged || !again.Duplicate || again.Digest != first.Digest {
		t.Fatalf("identical repeat: %+v %v", again, err)
	}
	conflicting := localEvidence(t, policy, "op-1", started, func(e *LocalOperationEvidence) {
		e.Report, e.Outcome, e.Steps = guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: "something else"}, "", nil
	})
	if _, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, conflicting); err == nil {
		t.Fatal("a different record for a committed revision was accepted")
	}
	rebound := localEvidence(t, policy, "op-1", started, func(e *LocalOperationEvidence) {
		e.Controls, e.Steps = []string{ControlBruteForce}, nil
		e.Report = guardbinding.ResultReport{Revision: 2, State: guardbinding.ReportResolved, Resolution: guardbinding.ResolutionObservedConverged, PriorDigest: first.Digest}
		e.Outcome = ""
	})
	if _, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, rebound); err == nil {
		t.Fatal("a resolution that changes the operation's binding was accepted")
	}
	resolved := localEvidence(t, policy, "op-1", started, func(e *LocalOperationEvidence) {
		e.Report, e.Outcome, e.Steps = guardbinding.ResultReport{Revision: 2, State: guardbinding.ReportResolved, Resolution: guardbinding.ResolutionObservedConverged, PriorDigest: first.Digest}, "", nil
	})
	if ack, err := svc.AcceptLocalOperation(ctx, ident, "node", 2, resolved); err != nil || ack.Duplicate {
		t.Fatalf("resolution: %+v %v", ack, err)
	}
	repairs, _ := svc.Repairs(ctx, ident.TenantID, ident.WorkerID, 0)
	if len(repairs) != 1 || repairs[0].Status != RepairSucceeded || repairs[0].Mode != RepairLocal || repairs[0].Local.Revision != 2 {
		t.Fatalf("records after resolution: %+v", repairs)
	}
}
