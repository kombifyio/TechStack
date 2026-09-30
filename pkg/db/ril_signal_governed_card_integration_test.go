package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/signals"
	"github.com/google/uuid"
)

func TestIntegrationRILSignalGovernedCardFollowsRemediationPolicy(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	database, err := Open(Config{Backend: StoreBackendPostgres, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	tenantID, ownerID := "card-tenant-"+suffix, "auth0|card-owner-"+suffix
	stackID, serverID := "card-stack-"+suffix, "card-server-"+suffix
	if _, err := database.ExecContext(ctx, `INSERT INTO techstack_tenants (id, display_name) VALUES ($1,'Card tenant')`, tenantID); err != nil {
		t.Fatal(err)
	}
	withTenantTx(ctx, t, database.DB, tenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status) VALUES ($1,$2,$3,$1,'active')`, stackID, tenantID, ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers (id, tenant_id, stack_id, owner_subject_id, name) VALUES ($1,$2,$3,$4,'Card server')`, serverID, tenantID, stackID, ownerID); err != nil {
			t.Fatal(err)
		}
	})

	// The template resolves the stack through the Emit transaction, so it can
	// only see the signal tenant's server row.
	policy := signals.RemediationPolicy{"ContainerDown": func(ctx context.Context, tx *sql.Tx, subject signals.RemediationSubject) (actions.ActionTemplate, bool, error) {
		var stack string
		if err := tx.QueryRowContext(ctx, `SELECT stack_id FROM servers WHERE tenant_id=$1 AND id=$2`, subject.TenantID, subject.ServerID).Scan(&stack); err != nil {
			return actions.ActionTemplate{}, false, err
		}
		return actions.ActionTemplate{
			StackID:          stack,
			Primitive:        rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: "sha256:" + fixedHex('a'), OperationClass: "verification"},
			ResolvedPlanHash: "sha256:" + fixedHex('b'),
			Target:           rilaction.TargetBinding{Scope: rilaction.TargetScopeRuntimeInstance, RuntimeInstanceRef: subject.ServerID},
			EvidenceSinkRef:  "evidence:" + subject.ServerID,
		}, true, nil
	}}
	outbox := signals.NewPostgresOutboxWithPolicy(database.DB, policy)
	authority := actions.NewPostgresAuthority(database.DB)
	observation := func(rule, dedupe string) signals.Observation {
		return signals.Observation{
			DedupeKey: dedupe, TenantID: tenantID, UserID: ownerID, ServerID: serverID,
			Source: signals.SourceHealth, Severity: signals.SeverityHigh, RecommendedAction: "Container is not running",
			TraceID: "trace-" + dedupe, AuditID: "audit-" + dedupe, ReceivedAt: time.Now().UTC(),
			AlertRule: rule, Title: rule, ServiceID: "app_web_1",
		}
	}

	remediable := observation("ContainerDown", "monitor-remediable")
	t.Run("remediable alert commits an owned card with an actionable envelope", func(t *testing.T) {
		record, inserted, err := outbox.Emit(ctx, remediable)
		if err != nil || !inserted || !record.Envelope.Actionable || record.Envelope.ServiceID != "app_web_1" || record.Envelope.Title != "ContainerDown" {
			t.Fatalf("Emit = record:%+v inserted:%t error:%v", record, inserted, err)
		}
		card, err := authority.Get(ctx, tenantID, ownerID, record.Envelope.ActionCardID)
		if err != nil || card.ServerID != serverID || card.Template.StackID != stackID || card.Status != "awaiting_grant" {
			t.Fatalf("governed card = %+v, %v", card, err)
		}
		if _, err := authority.Get(ctx, tenantID, "auth0|other-owner", record.Envelope.ActionCardID); !errors.Is(err, actions.ErrCardNotFound) {
			t.Fatalf("card visible to another owner: %v", err)
		}
	})

	t.Run("re-emitting the signal creates no duplicate card", func(t *testing.T) {
		replay, inserted, err := outbox.Emit(ctx, remediable)
		if err != nil || inserted || !replay.Envelope.Actionable {
			t.Fatalf("replay Emit = record:%+v inserted:%t error:%v", replay, inserted, err)
		}
		cards, err := authority.List(ctx, tenantID, ownerID)
		if err != nil || len(cards) != 1 {
			t.Fatalf("cards after replay = %d, %v", len(cards), err)
		}
	})

	t.Run("non-remediable alert stays notification-only", func(t *testing.T) {
		record, inserted, err := outbox.Emit(ctx, observation("HighCPU", "monitor-notification"))
		if err != nil || !inserted || record.Envelope.Actionable {
			t.Fatalf("Emit = record:%+v inserted:%t error:%v", record, inserted, err)
		}
		if _, err := authority.Get(ctx, tenantID, ownerID, record.Envelope.ActionCardID); !errors.Is(err, actions.ErrCardNotFound) {
			t.Fatalf("non-remediable card lookup error = %v, want ErrCardNotFound", err)
		}
	})

	t.Run("a failed card insert keeps the signal notification-only", func(t *testing.T) {
		clash := observation("ContainerDown", "monitor-clash")
		clash.SignalID = "ril-signal:clash-" + suffix
		cardID := "ril-action-card:" + clash.SignalID
		if _, err := authority.Create(ctx, actions.CreateGovernedCard{
			ID: cardID, TenantID: tenantID, OwnerSubjectID: ownerID,
			ServerID: serverID, Title: "pre-existing", Template: actions.ActionTemplate{StackID: stackID},
		}); err != nil {
			t.Fatal(err)
		}
		record, inserted, err := outbox.Emit(ctx, clash)
		if err != nil || !inserted || record.Envelope.Actionable {
			t.Fatalf("Emit = record:%+v inserted:%t error:%v", record, inserted, err)
		}
		replay, _, err := outbox.Emit(ctx, clash)
		if err != nil || replay.Envelope.Actionable {
			t.Fatalf("stored envelope after card failure = %+v, %v", replay.Envelope, err)
		}
		card, err := authority.Get(ctx, tenantID, ownerID, cardID)
		if err != nil || card.Title != "pre-existing" {
			t.Fatalf("pre-existing card changed: %+v, %v", card, err)
		}
	})
}

func withTenantTx(ctx context.Context, t *testing.T, database *sql.DB, tenantID string, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func fixedHex(char byte) string {
	out := make([]byte, 64)
	for index := range out {
		out[index] = char
	}
	return string(out)
}
