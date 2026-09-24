package providercontrol

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestPostgresProvisionResolutionAdoptsExactlyOneCandidateAtomically(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, binding, claimCount, guardCount := startGuardedIntegrationProvision(t, ledger)
	store, err := NewPostgresProvisionResolutionStore(ledger, allowProvisionResolutionVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	request := provisionDiscoveryRequest(record, binding, 1)
	observation, created, err := store.RecordProvisionDiscovery(t.Context(), request)
	if err != nil || !created {
		t.Fatalf("RecordProvisionDiscovery created=%v error=%v", created, err)
	}
	loaded, found, err := store.LoadProvisionDiscovery(
		t.Context(), request.TenantID, request.OperationID, request.IdempotencyKey,
	)
	if err != nil || !found || loaded.SnapshotDigest != observation.SnapshotDigest {
		t.Fatalf("LoadProvisionDiscovery found=%v snapshot=%q error=%v", found, loaded.SnapshotDigest, err)
	}
	replay, created, err := store.RecordProvisionDiscovery(t.Context(), request)
	if err != nil || created || replay.SnapshotDigest != observation.SnapshotDigest {
		t.Fatalf("discovery replay created=%v observation=%+v error=%v", created, replay, err)
	}
	if !replay.GuardedAt.Equal(observation.GuardedAt) ||
		provisionDiscoverySnapshotDigest(replay) != replay.SnapshotDigest {
		t.Fatalf("persisted discovery lost guard binding: %+v", replay)
	}

	decisionRequest := mustBuildProvisionResolutionRequest(t, record, observation, "adopt-decision-1")
	type resolutionResult struct {
		decision ProvisionResolutionDecision
		record   OperationRecord
		created  bool
		err      error
	}
	results := make(chan resolutionResult, 2)
	for range 2 {
		go func() {
			resolved, current, wasCreated, resolveErr := store.CommitProvisionResolution(t.Context(), decisionRequest)
			results <- resolutionResult{decision: resolved, record: current, created: wasCreated, err: resolveErr}
		}()
	}
	var decision ProvisionResolutionDecision
	var adopted OperationRecord
	createdCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent CommitProvisionResolution error=%v", result.err)
		}
		if result.created {
			createdCount++
		}
		if decision.DecisionDigest != "" && decision.DecisionDigest != result.decision.DecisionDigest {
			t.Fatalf("concurrent decisions differ: %q/%q", decision.DecisionDigest, result.decision.DecisionDigest)
		}
		decision = result.decision
		adopted = result.record
	}
	if createdCount != 1 {
		t.Fatalf("concurrent decision creates = %d, want 1", createdCount)
	}
	if decision.Outcome != ProvisionResolutionAdoptedExactCandidate ||
		adopted.Head.Phase != providerexecutor.PhaseResourcesBound || len(adopted.Head.Resources) != 1 {
		t.Fatalf("decision=%+v adopted head=%+v", decision, adopted.Head)
	}
	if decision.DecisionDigest != provisionResolutionDecisionDigest(decision) ||
		decision.ResultReceiptDigest != adopted.Head.ReceiptDigest {
		t.Fatalf("decision digest does not bind adopted receipt: %+v", decision)
	}
	replayedDecision, replayedRecord, created, err := store.CommitProvisionResolution(t.Context(), decisionRequest)
	if err != nil || created || replayedDecision.DecisionDigest != decision.DecisionDigest ||
		replayedRecord.Head.ReceiptDigest != adopted.Head.ReceiptDigest {
		t.Fatalf("decision replay created=%v decision=%+v record=%+v error=%v", created, replayedDecision, replayedRecord.Head, err)
	}

	assertProvisionResolutionCustodyCounts(t, ledger, record, claimCount, guardCount)
	assertProvisionResolutionRowsImmutable(t, ledger, record)
}

func TestPostgresProvisionResolutionZeroRemainsManualAndManyIsTerminal(t *testing.T) {
	for _, test := range []struct {
		name       string
		candidates int
		want       ProvisionResolutionOutcome
	}{
		{name: "zero", candidates: 0, want: ProvisionResolutionNoCandidateObserved},
		{name: "many", candidates: 2, want: ProvisionResolutionMultipleCandidatesQuarantined},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openProviderControlIntegrationDB(t)
			ledger, err := newIsolatedPostgresLedger(db, nil)
			if err != nil {
				t.Fatal(err)
			}
			record, binding, claimCount, guardCount := startGuardedIntegrationProvision(t, ledger)
			store, err := NewPostgresProvisionResolutionStore(ledger, allowProvisionResolutionVerifier{})
			if err != nil {
				t.Fatal(err)
			}
			observation, _, err := store.RecordProvisionDiscovery(
				t.Context(), provisionDiscoveryRequest(record, binding, test.candidates),
			)
			if err != nil {
				t.Fatal(err)
			}
			decision, current, created, err := store.CommitProvisionResolution(
				t.Context(), mustBuildProvisionResolutionRequest(t, record, observation, "non-adopt-decision"),
			)
			if err != nil || !created || decision.Outcome != test.want {
				t.Fatalf("decision=%+v created=%v error=%v", decision, created, err)
			}
			if current.Head.ReceiptDigest != record.Head.ReceiptDigest || current.AutomationState != OperationAutomationManualReconcileRequired {
				t.Fatalf("non-adoption changed head: %+v", current)
			}
			assertProvisionResolutionCustodyCounts(t, ledger, record, claimCount, guardCount)
			if test.candidates == 0 {
				binding.ResolutionRevision = 1
				second := provisionDiscoveryRequest(record, binding, 1)
				second.IdempotencyKey = "discovery-after-zero"
				second.ObservationRef = "provider-evidence://ionos/discovery/observation-after-zero"
				second.ObservationDigest = digest("observation-after-zero")
				second.AttestationRef = "provider-attestation://ionos/discovery/observation-after-zero"
				second.AttestationDigest = digest("attestation-after-zero")
				secondObservation, _, err := store.RecordProvisionDiscovery(t.Context(), second)
				if err != nil {
					t.Fatalf("read-only discovery after zero: %v", err)
				}
				secondDecision, adopted, _, err := store.CommitProvisionResolution(
					t.Context(), mustBuildProvisionResolutionRequest(t, record, secondObservation, "adopt-after-zero"),
				)
				if err != nil || secondDecision.Outcome != ProvisionResolutionAdoptedExactCandidate ||
					adopted.Head.Phase != providerexecutor.PhaseResourcesBound {
					t.Fatalf("adoption after zero decision=%+v head=%+v error=%v", secondDecision, adopted.Head, err)
				}
				assertProvisionResolutionCustodyCounts(t, ledger, record, claimCount, guardCount)
			} else {
				binding.ResolutionRevision = 1
				second := provisionDiscoveryRequest(record, binding, 0)
				second.IdempotencyKey = "discovery-after-quarantine"
				if _, _, err := store.RecordProvisionDiscovery(t.Context(), second); err == nil {
					t.Fatal("terminal quarantine admitted another discovery")
				}
			}
		})
	}
}

func TestPostgresProvisionDiscoverySameKeyReplaysAfterTerminalDecision(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, binding, _, _ := startGuardedIntegrationProvision(t, ledger)
	blocker := newBlockingProvisionDiscoveryVerifier()
	t.Cleanup(blocker.release)
	slowStore, err := NewPostgresProvisionResolutionStore(ledger, blocker)
	if err != nil {
		t.Fatal(err)
	}
	fastStore, err := NewPostgresProvisionResolutionStore(ledger, allowProvisionResolutionVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	request := provisionDiscoveryRequest(record, binding, 2)
	type discoveryResult struct {
		observation ProvisionDiscoveryObservation
		created     bool
		err         error
	}
	slowResult := make(chan discoveryResult, 1)
	go func() {
		observation, created, recordErr := slowStore.RecordProvisionDiscovery(t.Context(), request)
		slowResult <- discoveryResult{observation: observation, created: created, err: recordErr}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow discovery did not reach verifier")
	}

	observation, created, err := fastStore.RecordProvisionDiscovery(t.Context(), request)
	if err != nil || !created {
		t.Fatalf("fast discovery created=%v error=%v", created, err)
	}
	decision, _, created, err := fastStore.CommitProvisionResolution(
		t.Context(), mustBuildProvisionResolutionRequest(t, record, observation, "terminal-decision-before-discovery-replay"),
	)
	if err != nil || !created || decision.Outcome != ProvisionResolutionMultipleCandidatesQuarantined {
		t.Fatalf("terminal decision=%+v created=%v error=%v", decision, created, err)
	}
	blocker.release()
	select {
	case result := <-slowResult:
		if result.err != nil || result.created || result.observation.SnapshotDigest != observation.SnapshotDigest {
			t.Fatalf("late same-key replay created=%v observation=%+v error=%v", result.created, result.observation, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late same-key discovery replay did not finish")
	}
}

func mustBuildProvisionResolutionRequest(
	t *testing.T,
	record OperationRecord,
	observation ProvisionDiscoveryObservation,
	idempotencyKey string,
) ProvisionResolutionRequest {
	t.Helper()
	request, err := BuildProvisionResolutionRequest(
		record,
		observation,
		"operator-admin-1",
		idempotencyKey,
		"adopt-provision:"+record.Command.OperationID,
	)
	if err != nil {
		t.Fatalf("BuildProvisionResolutionRequest: %v", err)
	}
	return request
}

func startGuardedIntegrationProvision(
	t *testing.T,
	ledger *PostgresLedger,
) (OperationRecord, provisionDiscoveryBinding, int, int) {
	t.Helper()
	record := startAcceptedIntegrationAMOProvision(t, ledger)
	prepared := preparedBindingForCommandHead(t, record.Command, record.Head)
	grant, err := ledger.AcquireProvisionDispatchClaim(
		t.Context(), record.Command, record.Head, prepared,
		"operator-resolution-dispatch", "operator-resolution-dispatch-token", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.ReleaseExecutionClaim(t.Context(), grant.Claim); err != nil {
		t.Fatal(err)
	}
	var guardedAt time.Time
	var claimCount, guardCount int
	if err := ledger.withTenant(t.Context(), record.Command.TenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), `
			SELECT guarded_at FROM provider_provision_dispatch_guards
			WHERE tenant_id = $1 AND operation_id = $2
		`, record.Command.TenantID, record.Command.OperationID).Scan(&guardedAt); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), `
			SELECT
				(SELECT count(*) FROM provider_operation_execution_claims WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM provider_provision_dispatch_guards WHERE tenant_id = $1 AND operation_id = $2)
		`, record.Command.TenantID, record.Command.OperationID).Scan(&claimCount, &guardCount)
	}); err != nil {
		t.Fatal(err)
	}
	record, err = ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return record, provisionDiscoveryBinding{
		LeaseID: record.Command.LeaseID, LeaseRevision: record.Command.LeaseRevision,
		RuntimeServerID: record.Command.RuntimeServerID, ResourceGenerationID: record.Command.ResourceGenerationID,
		HeadSequence: record.Head.Sequence, HeadReceiptDigest: record.Head.ReceiptDigest,
		AdapterManifestHash: record.ExecutionProfile.AdapterManifestHash,
		PreparedBinding:     prepared, GuardedAt: guardedAt,
	}, claimCount, guardCount
}

func assertProvisionResolutionCustodyCounts(
	t *testing.T,
	ledger *PostgresLedger,
	record OperationRecord,
	wantClaims, wantGuards int,
) {
	t.Helper()
	if err := ledger.withTenant(t.Context(), record.Command.TenantID, func(tx *sql.Tx) error {
		var claims, guards int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
				(SELECT count(*) FROM provider_operation_execution_claims WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM provider_provision_dispatch_guards WHERE tenant_id = $1 AND operation_id = $2)
		`, record.Command.TenantID, record.Command.OperationID).Scan(&claims, &guards); err != nil {
			return err
		}
		if claims != wantClaims || guards != wantGuards {
			t.Fatalf("custody counts claims=%d guards=%d, want %d/%d", claims, guards, wantClaims, wantGuards)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertProvisionResolutionRowsImmutable(t *testing.T, ledger *PostgresLedger, record OperationRecord) {
	t.Helper()
	tx, err := ledger.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, record.Command.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE provider_provision_resolution_decisions SET operator_subject_id = 'rewritten'
		WHERE tenant_id = $1 AND operation_id = $2
	`, record.Command.TenantID, record.Command.OperationID); err == nil {
		t.Fatal("resolution decision update succeeded")
	}
}

type blockingProvisionDiscoveryVerifier struct {
	entered     chan struct{}
	releaseGate chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingProvisionDiscoveryVerifier() *blockingProvisionDiscoveryVerifier {
	return &blockingProvisionDiscoveryVerifier{
		entered: make(chan struct{}), releaseGate: make(chan struct{}),
	}
}

func (verifier *blockingProvisionDiscoveryVerifier) release() {
	verifier.releaseOnce.Do(func() { close(verifier.releaseGate) })
}

func (verifier *blockingProvisionDiscoveryVerifier) VerifyProvisionDiscovery(
	ctx context.Context,
	_ OperationRecord,
	_ ProvisionDiscoveryObservation,
) error {
	verifier.enterOnce.Do(func() { close(verifier.entered) })
	select {
	case <-verifier.releaseGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingProvisionDiscoveryVerifier) VerifyProvisionDecision(
	context.Context,
	OperationRecord,
	ProvisionDiscoveryObservation,
	ProvisionResolutionRequest,
) error {
	return nil
}
