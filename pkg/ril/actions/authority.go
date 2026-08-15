package actions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/google/uuid"
)

var (
	ErrCardNotFound               = errors.New("RIL action card not found")
	ErrCardConflict               = errors.New("RIL action card state conflict")
	ErrApprovalRequired           = errors.New("RIL action card approval required")
	ErrGrantRequired              = errors.New("RIL action card delegated grant required")
	ErrConnectorBindingRequired   = errors.New("RIL action card exact connector binding required")
	ErrConnectorGrantInsufficient = errors.New("RIL action card connector grant insufficient")
	ErrExecutionAdmission         = errors.New("RIL action execution admission rejected")
	ErrExecutionInProgress        = errors.New("RIL action card execution in progress")
)

type ActionTemplate struct {
	StackID          string                       `json:"stack_id"`
	Primitive        rilaction.PrimitiveBinding   `json:"primitive"`
	ResolvedPlanHash string                       `json:"resolved_plan_hash"`
	Grant            *rilaction.GrantBinding      `json:"grant,omitempty"`
	ConnectorBinding *ConnectorBindingExpectation `json:"connector_binding,omitempty"`
	Target           rilaction.TargetBinding      `json:"target"`
	Inputs           []rilaction.Input            `json:"inputs,omitempty"`
	EvidenceSinkRef  string                       `json:"evidence_sink_ref"`
}

type GovernedCard struct {
	ID                 string              `json:"id"`
	TenantID           string              `json:"-"`
	OwnerSubjectID     string              `json:"-"`
	ServerID           string              `json:"server_id"`
	Title              string              `json:"title"`
	Severity           string              `json:"severity"`
	Status             string              `json:"status"`
	Template           ActionTemplate      `json:"action"`
	Approval           *ApprovalRecord     `json:"approval,omitempty"`
	ExecutionID        string              `json:"execution_id,omitempty"`
	TraceID            string              `json:"trace_id,omitempty"`
	IdempotencyKey     string              `json:"-"`
	Evidence           *rilaction.Evidence `json:"evidence,omitempty"`
	ExecutionAdmission *ExecutionAdmission `json:"-"`
	ErrorCode          string              `json:"error_code,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	ApprovedAt         *time.Time          `json:"approved_at,omitempty"`
	DeniedAt           *time.Time          `json:"denied_at,omitempty"`
	ExecutionStartedAt *time.Time          `json:"execution_started_at,omitempty"`
	CompletedAt        *time.Time          `json:"completed_at,omitempty"`
}

type ApprovalRecord struct {
	ReceiptRef       string                  `json:"receipt_ref"`
	ReceiptHash      string                  `json:"receipt_hash"`
	ActorSubjectID   string                  `json:"actor_subject_id"`
	Class            rilaction.ApprovalClass `json:"class"`
	AuditCorrelation string                  `json:"audit_correlation_id"`
	ApprovedAt       time.Time               `json:"approved_at"`
	ValidUntil       time.Time               `json:"valid_until"`
}

type CreateGovernedCard struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	ServerID       string
	Title          string
	Severity       string
	Template       ActionTemplate
}

type BeginExecution struct {
	TenantID            string
	OwnerSubjectID      string
	CardID              string
	ExecutionID         string
	TraceID             string
	IdempotencyKey      string
	Now                 time.Time
	ConnectorProjection *ConnectorBindingProjection
}

type BeginDisposition string

const (
	BeginAcquired BeginDisposition = "acquired"
	BeginReplay   BeginDisposition = "replay"
)

type BeginResult struct {
	Disposition BeginDisposition
	Card        *GovernedCard
	Request     rilaction.Request
	Admission   ExecutionAdmission
}

type Authority interface {
	Create(context.Context, CreateGovernedCard) (*GovernedCard, error)
	Get(context.Context, string, string, string) (*GovernedCard, error)
	List(context.Context, string, string) ([]GovernedCard, error)
	Approve(context.Context, string, string, string, string, time.Time) (*GovernedCard, error)
	Deny(context.Context, string, string, string, string, time.Time) (*GovernedCard, error)
	Begin(context.Context, BeginExecution) (BeginResult, error)
	Complete(context.Context, string, string, rilaction.Evidence, string, time.Time) (*GovernedCard, error)
}

type PostgresAuthority struct{ db *sql.DB }

func NewPostgresAuthority(db *sql.DB) *PostgresAuthority { return &PostgresAuthority{db: db} }

func (s *PostgresAuthority) Create(ctx context.Context, input CreateGovernedCard) (*GovernedCard, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("ril actions: database not configured")
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	input.TenantID, input.OwnerSubjectID, input.ServerID = strings.TrimSpace(input.TenantID), strings.TrimSpace(input.OwnerSubjectID), strings.TrimSpace(input.ServerID)
	input.Title, input.Severity = strings.TrimSpace(input.Title), strings.TrimSpace(input.Severity)
	if input.TenantID == "" || input.OwnerSubjectID == "" || input.ServerID == "" || input.Title == "" || strings.TrimSpace(input.Template.StackID) == "" {
		return nil, fmt.Errorf("ril actions: tenant, owner, server, stack, and title required")
	}
	if input.Severity == "" {
		input.Severity = "info"
	}
	if err := validateConnectorBindingExpectation(input.Template.ConnectorBinding); err != nil {
		return nil, err
	}
	templateJSON, err := json.Marshal(input.Template)
	if err != nil {
		return nil, err
	}
	status := "awaiting_approval"
	if input.Template.Grant == nil {
		status = "awaiting_grant"
	}
	var out *GovernedCard
	err = s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		card, scanErr := scanGovernedCard(tx.QueryRowContext(ctx, `
			INSERT INTO ril_action_cards (
				id, tenant_id, owner_subject_id, server_id, stack_id, title,
				status, severity, action_json, decision_json, action_template_json
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'{}'::jsonb,'{}'::jsonb,$9::jsonb)
			RETURNING `+governedCardColumns,
			input.ID, input.TenantID, input.OwnerSubjectID, input.ServerID,
			input.Template.StackID, input.Title, status, input.Severity, templateJSON))
		if scanErr == nil {
			scanErr = appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
				TenantID: input.TenantID, CardID: input.ID, ToStatus: status,
				CorrelationID: newAuditCorrelation(""), ActorSubjectID: input.OwnerSubjectID,
				OccurredAt: card.CreatedAt,
			})
		}
		out = card
		return scanErr
	})
	return out, err
}

func (s *PostgresAuthority) Get(ctx context.Context, tenantID, ownerID, cardID string) (*GovernedCard, error) {
	var out *GovernedCard
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		card, err := scanGovernedCard(tx.QueryRowContext(ctx, `SELECT `+governedCardColumns+` FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3`, tenantID, ownerID, cardID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCardNotFound
		}
		out = card
		return err
	})
	return out, err
}

func (s *PostgresAuthority) List(ctx context.Context, tenantID, ownerID string) ([]GovernedCard, error) {
	var out []GovernedCard
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+governedCardColumns+` FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 ORDER BY created_at DESC`, tenantID, ownerID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			card, err := scanGovernedCard(rows)
			if err != nil {
				return err
			}
			out = append(out, *card)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresAuthority) Approve(ctx context.Context, tenantID, ownerID, cardID, correlationID string, now time.Time) (*GovernedCard, error) {
	correlationID = strings.TrimSpace(correlationID)
	if correlationID == "" {
		correlationID = "audit-" + uuid.NewString()
	}
	receiptRef := "approval:" + cardID
	receiptHash := digestText(receiptRef + "\x00" + ownerID + "\x00" + correlationID + "\x00" + now.Format(time.RFC3339Nano))
	record := ApprovalRecord{ReceiptRef: receiptRef, ReceiptHash: receiptHash, ActorSubjectID: ownerID, Class: rilaction.ApprovalClassOwnerStepUp, AuditCorrelation: correlationID, ApprovedAt: now, ValidUntil: now.Add(rilaction.MaxAuthorityValidity)}
	data, _ := json.Marshal(record)
	return s.transition(ctx, tenantID, ownerID, cardID, `status='awaiting_approval'`, "approved", `approval_json=$5::jsonb, approved_at=$6`, data, now)
}

func (s *PostgresAuthority) Deny(ctx context.Context, tenantID, ownerID, cardID, correlationID string, now time.Time) (*GovernedCard, error) {
	correlationID = newAuditCorrelation(correlationID)
	data, _ := json.Marshal(map[string]any{"decision": "denied", "actor_subject_id": ownerID, "audit_correlation_id": correlationID, "denied_at": now})
	return s.transition(ctx, tenantID, ownerID, cardID, `status IN ('awaiting_grant','awaiting_approval')`, "denied", `approval_json=$5::jsonb, denied_at=$6`, data, now)
}

func (s *PostgresAuthority) transition(ctx context.Context, tenantID, ownerID, cardID, statusPredicate, to, set string, data []byte, now time.Time) (*GovernedCard, error) {
	var out *GovernedCard
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var from string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 FOR UPDATE`, tenantID, ownerID, cardID).Scan(&from); errors.Is(err, sql.ErrNoRows) {
			return ErrCardConflict
		} else if err != nil {
			return err
		}
		card, err := scanGovernedCard(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status=$4, `+set+`, updated_at=now() WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND `+statusPredicate+` RETURNING `+governedCardColumns, tenantID, ownerID, cardID, to, data, now))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCardConflict
		}
		if err != nil {
			return err
		}
		correlationID := auditCorrelationFromDecision(data)
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: tenantID, CardID: cardID, FromStatus: from, ToStatus: to,
			CorrelationID: correlationID, ActorSubjectID: ownerID, OccurredAt: now,
		}); err != nil {
			return err
		}
		out = card
		return nil
	})
	return out, err
}

//nolint:gocyclo // Admission keeps approval, grant, replay, and transactional fencing in one fail-closed path.
func (s *PostgresAuthority) Begin(ctx context.Context, input BeginExecution) (BeginResult, error) {
	if input.Now.Location() != time.UTC {
		return BeginResult{}, fmt.Errorf("ril actions: UTC clock required")
	}
	var out BeginResult
	err := s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		card, err := scanGovernedCard(tx.QueryRowContext(ctx, `SELECT `+governedCardColumns+` FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 FOR UPDATE`, input.TenantID, input.OwnerSubjectID, input.CardID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCardNotFound
		}
		if err != nil {
			return err
		}
		if card.Status == string(StatusCompleted) || card.Status == string(StatusFailed) {
			if card.IdempotencyKey != input.IdempotencyKey || card.Evidence == nil {
				return ErrCardConflict
			}
			out = BeginResult{Disposition: BeginReplay, Card: card}
			return nil
		}
		if card.Status == "executing" || card.Status == "verifying" {
			if card.IdempotencyKey == input.IdempotencyKey {
				return ErrExecutionInProgress
			}
			return ErrCardConflict
		}
		if card.Status != string(StatusApproved) || card.Approval == nil {
			return ErrApprovalRequired
		}
		if card.Template.Grant == nil {
			return ErrGrantRequired
		}
		if err := validateConnectorBinding(card, input); err != nil {
			return err
		}
		validUntil := input.Now.Add(rilaction.MaxRequestValidity)
		if grantUntil, parseErr := time.Parse(time.RFC3339Nano, card.Template.Grant.ValidUntil); parseErr != nil || !input.Now.Before(grantUntil) {
			return ErrGrantRequired
		} else if grantUntil.Before(validUntil) {
			validUntil = grantUntil
		}
		if card.Approval.ValidUntil.Before(validUntil) {
			validUntil = card.Approval.ValidUntil
		}
		request := rilaction.Request{APIVersion: rilaction.APIVersionV1Alpha1, ActionCardID: card.ID, ExecutionID: input.ExecutionID, TraceID: input.TraceID, TenantID: input.TenantID, StackID: card.Template.StackID, Primitive: card.Template.Primitive, ResolvedPlanHash: card.Template.ResolvedPlanHash, Approval: rilaction.ApprovalBinding{ReceiptRef: card.Approval.ReceiptRef, ReceiptHash: card.Approval.ReceiptHash, Decision: string(StatusApproved), Class: card.Approval.Class, ApprovedAt: card.Approval.ApprovedAt.Format(time.RFC3339Nano), ValidUntil: card.Approval.ValidUntil.Format(time.RFC3339Nano)}, Grant: *card.Template.Grant, Target: card.Template.Target, Inputs: card.Template.Inputs, EvidenceSinkRef: card.Template.EvidenceSinkRef, IssuedAt: input.Now.Format(time.RFC3339Nano), ValidUntil: validUntil.Format(time.RFC3339Nano), Nonce: "nonce-" + uuid.NewString(), IdempotencyKey: input.IdempotencyKey}
		admission, admittedRequest, admissionErr := admitExecutionTx(ctx, tx, card, request, input.Now)
		if admissionErr != nil {
			return admissionErr
		}
		request = admittedRequest
		requestJSON, _ := json.Marshal(request)
		card, err = scanGovernedCard(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status='executing', execution_request_json=$4::jsonb, execution_id=$5, trace_id=$6, idempotency_key=$7, execution_started_at=$8, admission_inventory_revision=$9, admission_server_revision=$10, admission_server_generation=$11, admission_lease_id=$12, admission_lease_revision=$13, admission_resource_generation_id=$14::uuid, execution_admission_digest=$15, updated_at=now() WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND status='approved' RETURNING `+governedCardColumns, input.TenantID, input.OwnerSubjectID, input.CardID, requestJSON, input.ExecutionID, input.TraceID, input.IdempotencyKey, input.Now, admission.InventoryRevision, admission.ServerRevision, admission.ServerGeneration, admission.LeaseID, admission.LeaseRevision, admission.ResourceGenerationID, admission.Digest))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCardConflict
		}
		if err != nil {
			return err
		}
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: input.TenantID, CardID: input.CardID, FromStatus: cardStatusBeforeExecution,
			ToStatus: "executing", CorrelationID: newAuditCorrelation(input.TraceID),
			ActorSubjectID: input.OwnerSubjectID, ExecutionID: input.ExecutionID,
			TraceID: input.TraceID, OccurredAt: input.Now,
		}); err != nil {
			return err
		}
		card.ExecutionAdmission = &admission
		out = BeginResult{Disposition: BeginAcquired, Card: card, Request: request, Admission: admission}
		return nil
	})
	return out, err
}

func (s *PostgresAuthority) Complete(ctx context.Context, tenantID, cardID string, evidence rilaction.Evidence, errorCode string, now time.Time) (*GovernedCard, error) {
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	status := "completed"
	if evidence.Status == "failed" {
		status = "failed"
	}
	errorCode = strings.TrimSpace(errorCode)
	var out *GovernedCard
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var from, ownerID string
		if err := tx.QueryRowContext(ctx, `SELECT status, owner_subject_id FROM ril_action_cards WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, cardID).Scan(&from, &ownerID); errors.Is(err, sql.ErrNoRows) {
			return ErrCardConflict
		} else if err != nil {
			return err
		}
		card, scanErr := scanGovernedCard(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status=$3,evidence_json=$4::jsonb,error_code=NULLIF($5,''),completed_at=$6,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status IN ('executing','verifying') AND execution_id=$7 RETURNING `+governedCardColumns, tenantID, cardID, status, data, errorCode, now, evidence.ExecutionID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrCardConflict
		}
		if scanErr != nil {
			return scanErr
		}
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: tenantID, CardID: cardID, FromStatus: from, ToStatus: status,
			CorrelationID: newAuditCorrelation(evidence.TraceID), ActorSubjectID: ownerID,
			ExecutionID: evidence.ExecutionID, TraceID: evidence.TraceID, OccurredAt: now,
		}); err != nil {
			return err
		}
		out = card
		return nil
	})
	return out, err
}

const cardStatusBeforeExecution = "approved"

type actionTransitionAudit struct {
	TenantID, CardID, FromStatus, ToStatus, CorrelationID, ActorSubjectID string
	ExecutionID, TraceID                                                  string
	OccurredAt                                                            time.Time
}

func appendActionTransitionAudit(ctx context.Context, tx *sql.Tx, audit actionTransitionAudit) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ril_action_transition_audit (
			tenant_id, action_card_id, from_status, to_status,
			audit_correlation_id, actor_subject_id, execution_id, trace_id, occurred_at
		) VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9)
	`, audit.TenantID, audit.CardID, audit.FromStatus, audit.ToStatus,
		audit.CorrelationID, audit.ActorSubjectID, audit.ExecutionID, audit.TraceID, audit.OccurredAt)
	return err
}

func newAuditCorrelation(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "audit-" + uuid.NewString()
}

func auditCorrelationFromDecision(data []byte) string {
	var decision struct {
		AuditCorrelationID string `json:"audit_correlation_id"`
	}
	if json.Unmarshal(data, &decision) == nil {
		return newAuditCorrelation(decision.AuditCorrelationID)
	}
	return newAuditCorrelation("")
}

const governedCardColumns = `id,tenant_id,owner_subject_id,COALESCE(server_id,''),title,severity,status,action_template_json::text,COALESCE(approval_json::text,''),COALESCE(execution_id,''),COALESCE(idempotency_key,''),COALESCE(trace_id,''),COALESCE(evidence_json::text,''),COALESCE(error_code,''),created_at,updated_at,approved_at,denied_at,execution_started_at,completed_at,admission_inventory_revision,admission_server_revision,admission_server_generation,COALESCE(admission_lease_id,''),admission_lease_revision,COALESCE(admission_resource_generation_id::text,''),COALESCE(execution_admission_digest,'')`

type scanner interface{ Scan(...any) error }

func scanGovernedCard(row scanner) (*GovernedCard, error) {
	var c GovernedCard
	var templateJSON, approvalJSON, evidenceJSON, admissionLeaseID, admissionGenerationID, admissionDigest string
	var admissionInventoryRevision, admissionServerRevision, admissionServerGeneration, admissionLeaseRevision sql.NullInt64
	err := row.Scan(&c.ID, &c.TenantID, &c.OwnerSubjectID, &c.ServerID, &c.Title, &c.Severity, &c.Status, &templateJSON, &approvalJSON, &c.ExecutionID, &c.IdempotencyKey, &c.TraceID, &evidenceJSON, &c.ErrorCode, &c.CreatedAt, &c.UpdatedAt, &c.ApprovedAt, &c.DeniedAt, &c.ExecutionStartedAt, &c.CompletedAt, &admissionInventoryRevision, &admissionServerRevision, &admissionServerGeneration, &admissionLeaseID, &admissionLeaseRevision, &admissionGenerationID, &admissionDigest)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(templateJSON), &c.Template); err != nil {
		return nil, err
	}
	if approvalJSON != "" {
		c.Approval = &ApprovalRecord{}
		if err = json.Unmarshal([]byte(approvalJSON), c.Approval); err != nil {
			return nil, err
		}
	}
	if evidenceJSON != "" {
		c.Evidence = &rilaction.Evidence{}
		if err = json.Unmarshal([]byte(evidenceJSON), c.Evidence); err != nil {
			return nil, err
		}
	}
	if admissionInventoryRevision.Valid || admissionServerRevision.Valid || admissionServerGeneration.Valid || admissionLeaseID != "" || admissionLeaseRevision.Valid || admissionGenerationID != "" || admissionDigest != "" {
		if !admissionInventoryRevision.Valid || !admissionServerRevision.Valid || !admissionServerGeneration.Valid || !admissionLeaseRevision.Valid || admissionInventoryRevision.Int64 < 1 || admissionServerRevision.Int64 < 1 || admissionServerGeneration.Int64 < 1 || admissionLeaseRevision.Int64 < 1 || admissionLeaseID == "" || admissionGenerationID == "" || admissionDigest == "" {
			return nil, fmt.Errorf("ril actions: incomplete persisted execution admission")
		}
		c.ExecutionAdmission = &ExecutionAdmission{
			InventoryRevision: admissionInventoryRevision.Int64,
			ServerRevision:    admissionServerRevision.Int64, ServerGeneration: admissionServerGeneration.Int64,
			LeaseID:       admissionLeaseID,
			LeaseRevision: uint64(admissionLeaseRevision.Int64), ResourceGenerationID: admissionGenerationID,
			Digest: admissionDigest,
		}
	}
	return &c, nil
}
func (s *PostgresAuthority) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ril actions: database not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", strings.TrimSpace(tenantID)); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

var _ Authority = (*PostgresAuthority)(nil)
