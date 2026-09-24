package providercontrol

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestLoadProvisionDecisionRejectsUnsafeStoredCounters(t *testing.T) {
	tests := []struct {
		name           string
		revision       int64
		headSequence   int64
		resultSequence any
	}{
		{name: "zero resolution revision", revision: 0, headSequence: 1},
		{name: "oversized resolution revision", revision: int64(providerexecutor.MaxJSONSafeInteger) + 1, headSequence: 1},
		{name: "zero head sequence", revision: 1, headSequence: 0},
		{name: "oversized head sequence", revision: 1, headSequence: int64(providerexecutor.MaxJSONSafeInteger) + 1},
		{name: "negative result sequence", revision: 1, headSequence: 1, resultSequence: int64(-1)},
		{name: "oversized result sequence", revision: 1, headSequence: 1, resultSequence: int64(providerexecutor.MaxJSONSafeInteger) + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer func() { _ = db.Close() }()

			mock.ExpectBegin()
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			mock.ExpectQuery("SELECT resolution_revision").
				WithArgs("tenant-1", "operation-1", "decision-1").
				WillReturnRows(provisionDecisionRows(test.revision, test.headSequence, test.resultSequence))

			_, found, err := loadProvisionDecisionByIdempotencyTx(
				t.Context(), tx, "tenant-1", "operation-1", "decision-1",
			)
			if found || !errors.Is(err, ErrProvisionResolutionConflict) {
				t.Fatalf("load decision = found %t, err %v; want conflict", found, err)
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestLoadProvisionDecisionAcceptsJSONSafeCounterBoundary(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	maximum := int64(providerexecutor.MaxJSONSafeInteger)
	mock.ExpectQuery("SELECT resolution_revision").
		WithArgs("tenant-1", "operation-1", "decision-1").
		WillReturnRows(provisionDecisionRows(maximum, maximum, maximum))

	decision, found, err := loadProvisionDecisionByIdempotencyTx(
		t.Context(), tx, "tenant-1", "operation-1", "decision-1",
	)
	if err != nil || !found {
		t.Fatalf("load decision = found %t, err %v", found, err)
	}
	if decision.ResolutionRevision != providerexecutor.MaxJSONSafeInteger ||
		decision.ExpectedHeadSequence != providerexecutor.MaxJSONSafeInteger ||
		decision.ResultReceiptSequence != providerexecutor.MaxJSONSafeInteger {
		t.Fatalf("decision counters = %+v", decision)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestNullableUint64PreservesValidatedJSONSafeRange(t *testing.T) {
	if value := nullableUint64(0); value != nil {
		t.Fatalf("nullableUint64(0) = %#v; want nil", value)
	}
	if value := nullableUint64(providerexecutor.MaxJSONSafeInteger); value != int64(providerexecutor.MaxJSONSafeInteger) {
		t.Fatalf("nullableUint64(max) = %#v", value)
	}
}

func TestImmediateJSONSafeSuccessorRejectsUnsafeStoredSequence(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous int64
		next     uint64
		want     bool
	}{
		{name: "successor", previous: 41, next: 42, want: true},
		{name: "negative stored value", previous: -1, next: 1},
		{name: "zero stored value", previous: 0, next: 1},
		{name: "not successor", previous: 41, next: 43},
		{name: "unsafe next", previous: int64(providerexecutor.MaxJSONSafeInteger), next: providerexecutor.MaxJSONSafeInteger + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isImmediateJSONSafeSuccessor(test.previous, test.next); got != test.want {
				t.Fatalf("isImmediateJSONSafeSuccessor(%d, %d) = %t, want %t", test.previous, test.next, got, test.want)
			}
		})
	}
}

func provisionDecisionRows(revision, headSequence int64, resultSequence any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"resolution_revision", "observation_id", "observation_snapshot_digest",
		"expected_head_sequence", "expected_head_receipt_digest", "outcome",
		"selected_candidate_digest", "operator_subject_id", "operator_attestation_ref",
		"operator_attestation_digest", "idempotency_key", "request_digest",
		"decision_digest", "result_receipt_sequence", "result_receipt_digest", "decided_at",
	}).AddRow(
		revision, "11111111-1111-4111-8111-111111111111", digest("observation-snapshot"),
		headSequence, digest("expected-head"), string(ProvisionResolutionNoCandidateObserved),
		nil, "operator-1", "provider-attestation://techstack/operators/decision-1",
		digest("operator-attestation"), "decision-1", digest("request"),
		digest("decision"), resultSequence, nil, contractNow,
	)
}
