package guardbootstrap

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func newTestIssuer(t *testing.T) (*EnrollmentIssuer, sqlmock.Sqlmock) {
	t.Helper()
	t.Setenv(EnvEnrollmentSeed, strings.Repeat("s", 48))
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io")
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	issuer, err := NewEnrollmentIssuer(database, func() time.Time { return now })
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return issuer, mock
}

func testRequest() EnrollmentRequest {
	return EnrollmentRequest{
		TenantID: "tenant-demo", OperationID: "op_58534d9e",
		LeaseID: "lease-c4e87761", StackID: "stack-abc123",
	}
}

// The capability record must be written insert-once. The pre-existing
// UpsertPairingToken path resets used_at and status from the excluded row, so
// reusing it here would let a provisioning replay re-open a capability the node
// had already spent — a one-time credential that is no longer one-time.
func TestRecordCapabilityIsInsertOnce(t *testing.T) {
	issuer, mock := newTestIssuer(t)
	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM servers").
		WillReturnRows(sqlmock.NewRows([]string{"owner_subject_id"}).AddRow("auth0|owner"))
	mock.ExpectExec(regexp.QuoteMeta("ON CONFLICT (tenant_id, token_hash) DO NOTHING")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := issuer.RecordCapability(t.Context(), testRequest()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database interaction: %v", err)
	}
}

// Redemption refuses a capability whose token carries no owner subject, so a
// stack without an owner must fail here rather than mint a capability that can
// never be redeemed and a VM that can never enrol.
func TestRecordCapabilityRefusesOwnerlessStack(t *testing.T) {
	for name, rows := range map[string]*sqlmock.Rows{
		"null owner":  sqlmock.NewRows([]string{"owner_subject_id"}).AddRow(nil),
		"blank owner": sqlmock.NewRows([]string{"owner_subject_id"}).AddRow("  "),
		"no server":   sqlmock.NewRows([]string{"owner_subject_id"}),
	} {
		t.Run(name, func(t *testing.T) {
			issuer, mock := newTestIssuer(t)
			mock.ExpectBegin()
			mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("FROM servers").WillReturnRows(rows)
			mock.ExpectRollback()

			if err := issuer.RecordCapability(t.Context(), testRequest()); err != ErrStackOwnerUnknown {
				t.Fatalf("err = %v, want ErrStackOwnerUnknown", err)
			}
		})
	}
}

// The payload is derived, so it must be reproducible without touching the
// database at all — that purity is what keeps the prepared request digest
// stable across a provisioning retry.
func TestRenderPayloadPerformsNoDatabaseWork(t *testing.T) {
	issuer, mock := newTestIssuer(t)
	first, err := issuer.RenderPayload(testRequest())
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := issuer.RenderPayload(testRequest())
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("RenderPayload is not deterministic")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("RenderPayload touched the database: %v", err)
	}
}

// A deployment with no derivation secret must refuse to construct rather than
// provision VMs that can never enrol.
func TestIssuerFailsClosedWithoutSeedOrOrigin(t *testing.T) {
	database, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer database.Close()

	t.Run("no seed", func(t *testing.T) {
		t.Setenv(EnvEnrollmentSeed, "")
		t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
		t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
		t.Setenv("SERVICE_AUTH_SECRET", "")
		t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io")
		if _, err := NewEnrollmentIssuer(database, nil); err != ErrEnrollmentSeedUnavailable {
			t.Fatalf("err = %v, want ErrEnrollmentSeedUnavailable", err)
		}
	})

	t.Run("no origin", func(t *testing.T) {
		t.Setenv(EnvEnrollmentSeed, strings.Repeat("s", 48))
		t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "")
		t.Setenv("TECHSTACK_PUBLIC_URL", "")
		t.Setenv("RENDER_EXTERNAL_URL", "")
		t.Setenv("RENDER_EXTERNAL_HOSTNAME", "")
		if _, err := NewEnrollmentIssuer(database, nil); err != ErrPublicOriginUnavailable {
			t.Fatalf("err = %v, want ErrPublicOriginUnavailable", err)
		}
	})
}
