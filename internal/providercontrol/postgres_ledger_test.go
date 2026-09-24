package providercontrol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestNewPostgresLedgerRequiresDatabase(t *testing.T) {
	if _, err := NewPostgresLedger(nil, nil); err == nil {
		t.Fatal("NewPostgresLedger accepted nil database")
	}
}

func TestPostgresLedgerLoadIsTenantScoped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	ledger, err := NewPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").WithArgs(tenantContextKey, "tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT o.tenant_id, o.operation_id, o.lease_id").
		WithArgs("tenant-1", "missing").
		WillReturnRows(operationRows())
	mock.ExpectRollback()
	if _, err := ledger.LoadOperation(t.Context(), "tenant-1", "missing"); err != ErrOperationNotFound {
		t.Fatalf("LoadOperation error = %v, want ErrOperationNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresLedgerRejectsDriftedHeadProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	ledger, err := NewPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	profile, snapshot, err := normalizeExecutionProfile(testProfile("managed-compute"))
	if err != nil {
		t.Fatalf("normalizeExecutionProfile: %v", err)
	}
	command, err := providerexecutor.SealCommand(providerexecutor.Command{
		Operation: providerexecutor.OperationPlan, TenantID: "tenant-1", LeaseID: "lease-1",
		LeaseRevision: 7, RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		IdempotencyKey: "plan-1", ProviderID: profile.ProviderID, AdapterID: profile.AdapterID,
		CustodyRef: "custody://tenant-1/provider-access", CustodyHash: digest("custody"),
		ConnectionRef: "provider-connection://tenant-1/managed-compute", ConnectionHash: digest("connection"),
		CapabilitySnapshotHash: profile.CapabilitySnapshotHash,
		ExecutionProfileHash:   profile.ExecutionProfileHash, ResourceGraphHash: digest("graph"),
		LedgerRevision: 3, DesiredSpecRef: "desired-spec://techstack/lease-1/revision-2",
		DesiredSpecHash: digest(`{"kit":"cloud"}`), RequestedAt: contractNow,
	})
	if err != nil {
		t.Fatalf("SealCommand: %v", err)
	}
	receipt, err := providerexecutor.InitialReceipt(command, contractNow)
	if err != nil {
		t.Fatalf("InitialReceipt: %v", err)
	}
	commandJSON, _ := json.Marshal(operationEnvelope{
		SchemaVersion:           operationEnvelopeVersion,
		ExecutionAuthority:      ExecutionAuthorityTechstackProviderControl,
		ExecutionProfile:        snapshot,
		RuntimeServerGeneration: 1,
		Command:                 command,
	})
	receiptJSON, _ := json.Marshal(receipt)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").WithArgs(tenantContextKey, "tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT o.tenant_id, o.operation_id, o.lease_id").
		WithArgs("tenant-1", command.OperationID).
		WillReturnRows(operationRows().AddRow(
			command.TenantID, command.OperationID, command.LeaseID, string(command.Operation),
			command.IdempotencyKey, command.AdapterID, digest("drifted-command-column"),
			int64(command.LedgerRevision), ProvisionDispatchNativeIdempotency, command.RequestedAt,
			int64(receipt.Sequence), receipt.ReceiptDigest, string(receipt.Status), string(receipt.Phase),
			int64(receipt.Sequence), receipt.ReceiptDigest, string(receipt.Status), string(receipt.Phase),
			commandJSON, receiptJSON, false, false,
		))
	mock.ExpectRollback()
	if _, err := ledger.LoadOperation(t.Context(), command.TenantID, command.OperationID); !errors.Is(err, ErrLedgerConflict) {
		t.Fatalf("LoadOperation error = %v, want ErrLedgerConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func operationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"tenant_id", "operation_id", "lease_id", "operation", "idempotency_key", "adapter_id",
		"command_digest", "ledger_revision", "provision_dispatch_mode", "requested_at", "head_sequence", "head_receipt_digest", "operation_status", "operation_phase",
		"receipt_sequence", "receipt_digest", "receipt_status", "receipt_phase", "command_json", "receipt_json", "dispatch_guarded", "resource_free_terminalized",
	})
}
