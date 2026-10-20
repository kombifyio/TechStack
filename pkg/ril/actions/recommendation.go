package actions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Recommendation/v1 lives on the action-card authority: the same table, tenant
// RLS, owner scope and append-only transition audit as governed actions, with
// its own card kind and lifecycle. Nothing runs before the owner accepts it
// (owner decision D2, 2026-10-19); acceptance revalidates against a fresh
// snapshot first.
const (
	RecommendationAPIVersion = "techstack.kombify.io/recommendation/v1"

	CardKindGovernedAction       = "governed-action"
	CardKindLayoutRecommendation = "layout-recommendation"

	RecommendationOpen       = "open"
	RecommendationAccepted   = "accepted"
	RecommendationRejected   = "rejected"
	RecommendationSnoozed    = "snoozed"
	RecommendationExpired    = "expired"
	RecommendationSuperseded = "superseded"
	RecommendationExecuting  = "executing"
	RecommendationDone       = "done"
	RecommendationFailed     = "failed"

	// DefaultRecommendationValidity bounds how long shown advice stays
	// acceptable without a new proposal.
	DefaultRecommendationValidity = 24 * time.Hour
	maxSnooze                     = 90 * 24 * time.Hour
)

var (
	ErrRecommendationNotFound = errors.New("recommendation not found")
	// ErrRecommendationDigest means the caller accepted advice it was not shown.
	ErrRecommendationDigest = errors.New("recommendation proposal digest mismatch")
	// ErrRecommendationState means the record is not in a state that allows
	// the requested transition.
	ErrRecommendationState = errors.New("recommendation state does not allow this transition")
)

// Recommendation is one durable, versioned piece of advice.
type Recommendation struct {
	ID             string `json:"id"`
	APIVersion     string `json:"apiVersion"`
	TenantID       string `json:"-"`
	OwnerSubjectID string `json:"-"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	StackID        string `json:"stack_id,omitempty"`
	Title          string `json:"title"`
	State          string `json:"state"`
	// ProposalDigest identifies exactly what was shown; SnapshotDigest the
	// homelab view it was computed from; MaterialDigest what acceptance
	// compares after re-planning (the decision, not the observation).
	ProposalDigest string          `json:"proposal_digest"`
	SnapshotDigest string          `json:"snapshot_digest"`
	MaterialDigest string          `json:"material_digest"`
	Payload        json.RawMessage `json:"payload"`
	ExpiresAt      time.Time       `json:"expires_at"`
	SnoozedUntil   *time.Time      `json:"snoozed_until,omitempty"`
	SupersededBy   string          `json:"superseded_by,omitempty"`
	Acceptance     *Acceptance     `json:"acceptance,omitempty"`
	ExecutionID    string          `json:"execution_id,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	AcceptedAt     *time.Time      `json:"accepted_at,omitempty"`
	RejectedAt     *time.Time      `json:"rejected_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

// Acceptance is the durable evidence of an owner acceptance, including the
// fresh snapshot the advice was revalidated against.
type Acceptance struct {
	ReceiptRef                 string    `json:"receipt_ref"`
	ReceiptHash                string    `json:"receipt_hash"`
	ActorSubjectID             string    `json:"actor_subject_id"`
	AuditCorrelation           string    `json:"audit_correlation_id"`
	AcceptedAt                 time.Time `json:"accepted_at"`
	RevalidatedSnapshotDigest  string    `json:"revalidated_snapshot_digest"`
	RevalidatedProposalDigest  string    `json:"revalidated_proposal_digest"`
	RevalidatedMaterialMatched bool      `json:"revalidated_material_matched"`
}

// ProposeRecommendation is advice computed by a planner.
type ProposeRecommendation struct {
	TenantID       string
	OwnerSubjectID string
	Scope          string
	StackID        string
	Title          string
	ProposalDigest string
	SnapshotDigest string
	MaterialDigest string
	Payload        json.RawMessage
	Validity       time.Duration
	Now            time.Time
	// Verified must be true: only advice that the verification authority
	// checked against exact inventory becomes a record.
	Verified bool
}

// AcceptRecommendation accepts shown advice. Fresh is the same question
// re-planned against a fresh snapshot by the caller.
type AcceptRecommendation struct {
	TenantID       string
	OwnerSubjectID string
	ID             string
	ProposalDigest string
	Fresh          ProposeRecommendation
	CorrelationID  string
	Now            time.Time
}

// AcceptOutcome reports either the accepted record or, after a material
// change, the superseded record and its replacement to show instead.
type AcceptOutcome struct {
	Accepted    *Recommendation
	Superseded  *Recommendation
	Replacement *Recommendation
	Replayed    bool
}

// RecommendationAuthority is the durable recommendation lifecycle.
type RecommendationAuthority interface {
	Propose(context.Context, ProposeRecommendation) (*Recommendation, error)
	GetRecommendation(ctx context.Context, tenantID, ownerID, id string, now time.Time) (*Recommendation, error)
	ListRecommendations(ctx context.Context, tenantID, ownerID, scope string, now time.Time) ([]Recommendation, error)
	Accept(context.Context, AcceptRecommendation) (AcceptOutcome, error)
	Reject(ctx context.Context, tenantID, ownerID, id, correlationID string, now time.Time) (*Recommendation, error)
	Snooze(ctx context.Context, tenantID, ownerID, id string, until time.Time, correlationID string, now time.Time) (*Recommendation, error)
	MarkExecuting(ctx context.Context, tenantID, ownerID, id, executionID string, now time.Time) (*Recommendation, error)
	Finish(ctx context.Context, tenantID, ownerID, id string, succeeded bool, errorCode string, now time.Time) (*Recommendation, error)
}

// acceptDecision is the pure acceptance rule, kept apart from SQL so the
// state machine is testable on its own.
type acceptDecision int

const (
	decideAccept acceptDecision = iota
	decideReplay
	decideSupersede
	decideRefuseDigest
	decideRefuseState
)

func decideAcceptance(current Recommendation, request AcceptRecommendation, now time.Time) acceptDecision {
	if request.ProposalDigest != current.ProposalDigest {
		return decideRefuseDigest
	}
	switch current.State {
	case RecommendationAccepted, RecommendationExecuting, RecommendationDone:
		// Only the run that accepted it may replay it; any other run must
		// see fresh advice rather than days-old assignments.
		if current.Acceptance != nil && strings.TrimSpace(request.CorrelationID) != "" &&
			current.Acceptance.AuditCorrelation == strings.TrimSpace(request.CorrelationID) {
			return decideReplay
		}
		return decideRefuseState
	case RecommendationOpen, RecommendationSnoozed:
	default:
		return decideRefuseState
	}
	if !now.Before(current.ExpiresAt) {
		// Stale advice is never accepted as shown; the owner reviews again.
		return decideRefuseState
	}
	if request.Fresh.MaterialDigest != current.MaterialDigest {
		return decideSupersede
	}
	return decideAccept
}

// lazyState applies time-driven transitions on read: expiry and the end of a
// snooze.
func lazyState(current Recommendation, now time.Time) string {
	switch current.State {
	case RecommendationOpen:
		if !now.Before(current.ExpiresAt) {
			return RecommendationExpired
		}
	case RecommendationSnoozed:
		if current.SnoozedUntil != nil && !now.Before(*current.SnoozedUntil) {
			if !now.Before(current.ExpiresAt) {
				return RecommendationExpired
			}
			return RecommendationOpen
		}
	}
	return current.State
}

func validateProposal(input ProposeRecommendation) error {
	if !input.Verified {
		return fmt.Errorf("recommendation: only verified advice can be recorded")
	}
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.OwnerSubjectID) == "" || strings.TrimSpace(input.Scope) == "" || strings.TrimSpace(input.Title) == "" {
		return fmt.Errorf("recommendation: tenant, owner, scope and title required")
	}
	for name, digest := range map[string]string{"proposal": input.ProposalDigest, "snapshot": input.SnapshotDigest, "material": input.MaterialDigest} {
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
			return fmt.Errorf("recommendation: %s digest must be sha256", name)
		}
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return fmt.Errorf("recommendation: payload must be JSON")
	}
	return nil
}

const recommendationColumns = `id,tenant_id,owner_subject_id,recommendation_scope,COALESCE(stack_id,''),title,status,proposal_digest,snapshot_digest,material_digest,recommendation_json::text,expires_at,snoozed_until,COALESCE(superseded_by,''),COALESCE(approval_json::text,''),COALESCE(execution_id,''),COALESCE(error_code,''),created_at,updated_at,approved_at,denied_at,completed_at`

func scanRecommendation(row scanner) (*Recommendation, error) {
	var r Recommendation
	var payload, acceptance string
	if err := row.Scan(&r.ID, &r.TenantID, &r.OwnerSubjectID, &r.Scope, &r.StackID, &r.Title, &r.State, &r.ProposalDigest, &r.SnapshotDigest, &r.MaterialDigest, &payload, &r.ExpiresAt, &r.SnoozedUntil, &r.SupersededBy, &acceptance, &r.ExecutionID, &r.ErrorCode, &r.CreatedAt, &r.UpdatedAt, &r.AcceptedAt, &r.RejectedAt, &r.CompletedAt); err != nil {
		return nil, err
	}
	r.APIVersion, r.Kind, r.Payload = RecommendationAPIVersion, "layout", json.RawMessage(payload)
	if acceptance != "" {
		r.Acceptance = &Acceptance{}
		if err := json.Unmarshal([]byte(acceptance), r.Acceptance); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// Propose stores advice once per scope and proposal. Live advice with the
// same digest is returned as it is; a rejection of the same decision keeps
// suppressing it; other live advice in the scope is superseded.
func (s *PostgresAuthority) Propose(ctx context.Context, input ProposeRecommendation) (*Recommendation, error) {
	if err := validateProposal(input); err != nil {
		return nil, err
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	validity := input.Validity
	if validity <= 0 {
		validity = DefaultRecommendationValidity
	}
	var out *Recommendation
	err := s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		if err := expireScope(ctx, tx, input.TenantID, input.OwnerSubjectID, input.Scope, now); err != nil {
			return err
		}
		existing, err := scanRecommendation(tx.QueryRowContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND card_kind='layout-recommendation' AND recommendation_scope=$3
			  AND ((status IN ('open','snoozed') AND proposal_digest=$4) OR (status='rejected' AND material_digest=$5))
			ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
			input.TenantID, input.OwnerSubjectID, input.Scope, input.ProposalDigest, input.MaterialDigest))
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id := newRecommendationID()
		if err := supersedeScope(ctx, tx, input, id, now); err != nil {
			return err
		}
		created, err := insertRecommendation(ctx, tx, id, input, now, validity)
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if isUniqueViolation(err) {
		// A concurrent proposal of the same advice won; return that record.
		return s.liveRecommendation(ctx, input)
	}
	return out, err
}

func newRecommendationID() string { return "rec-" + uuid.NewString() }

func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == "23505"
}

func (s *PostgresAuthority) liveRecommendation(ctx context.Context, input ProposeRecommendation) (*Recommendation, error) {
	var out *Recommendation
	err := s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		record, err := scanRecommendation(tx.QueryRowContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND card_kind='layout-recommendation' AND recommendation_scope=$3
			  AND status IN ('open','snoozed') AND proposal_digest=$4 LIMIT 1`,
			input.TenantID, input.OwnerSubjectID, input.Scope, input.ProposalDigest))
		out = record
		return err
	})
	return out, err
}

func insertRecommendation(ctx context.Context, tx *sql.Tx, id string, input ProposeRecommendation, now time.Time, validity time.Duration) (*Recommendation, error) {
	created, err := scanRecommendation(tx.QueryRowContext(ctx, `
		INSERT INTO ril_action_cards (
			id, tenant_id, owner_subject_id, stack_id, title, status, severity,
			action_json, decision_json, card_kind, recommendation_scope, recommendation_json,
			proposal_digest, snapshot_digest, material_digest, expires_at
		) VALUES ($1,$2,$3,NULLIF($4,''),$5,'open','info','{}'::jsonb,'{}'::jsonb,'layout-recommendation',$6,$7::jsonb,$8,$9,$10,$11)
		RETURNING `+recommendationColumns,
		id, input.TenantID, input.OwnerSubjectID, input.StackID, input.Title, input.Scope, string(input.Payload),
		input.ProposalDigest, input.SnapshotDigest, input.MaterialDigest, now.Add(validity)))
	if err != nil {
		return nil, err
	}
	if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
		TenantID: input.TenantID, CardID: id, ToStatus: RecommendationOpen,
		CorrelationID: newAuditCorrelation(""), ActorSubjectID: input.OwnerSubjectID, OccurredAt: now,
	}); err != nil {
		return nil, err
	}
	return created, nil
}

func supersedeScope(ctx context.Context, tx *sql.Tx, input ProposeRecommendation, replacementID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `UPDATE ril_action_cards SET status='superseded', superseded_by=$4, updated_at=now()
		WHERE tenant_id=$1 AND owner_subject_id=$2 AND card_kind='layout-recommendation' AND recommendation_scope=$3
		  AND status IN ('open','snoozed') AND id<>$4
		RETURNING id`, input.TenantID, input.OwnerSubjectID, input.Scope, replacementID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: input.TenantID, CardID: id, FromStatus: RecommendationOpen, ToStatus: RecommendationSuperseded,
			CorrelationID: newAuditCorrelation(""), ActorSubjectID: input.OwnerSubjectID, OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// expireScope applies the time-driven transitions of one scope durably.
func expireScope(ctx context.Context, tx *sql.Tx, tenantID, ownerID, scope string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
		WHERE tenant_id=$1 AND owner_subject_id=$2 AND card_kind='layout-recommendation'
		  AND ($3='' OR recommendation_scope=$3) AND status IN ('open','snoozed') FOR UPDATE`, tenantID, ownerID, scope)
	if err != nil {
		return err
	}
	var changed []Recommendation
	for rows.Next() {
		current, err := scanRecommendation(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if next := lazyState(*current, now); next != current.State {
			current.UpdatedAt = now
			changed = append(changed, *current)
			changed[len(changed)-1].State = next
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, next := range changed {
		if _, err := tx.ExecContext(ctx, `UPDATE ril_action_cards SET status=$4, snoozed_until=CASE WHEN $4='open' THEN NULL ELSE snoozed_until END, updated_at=now()
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3`, tenantID, ownerID, next.ID, next.State); err != nil {
			return err
		}
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: tenantID, CardID: next.ID, ToStatus: next.State,
			CorrelationID: newAuditCorrelation(""), ActorSubjectID: "system:recommendation-clock", OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// GetRecommendation reads one record of the owner.
func (s *PostgresAuthority) GetRecommendation(ctx context.Context, tenantID, ownerID, id string, now time.Time) (*Recommendation, error) {
	var out *Recommendation
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := expireScope(ctx, tx, tenantID, ownerID, "", now.UTC()); err != nil {
			return err
		}
		record, err := scanRecommendation(tx.QueryRowContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='layout-recommendation'`, tenantID, ownerID, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRecommendationNotFound
		}
		out = record
		return err
	})
	return out, err
}

// ListRecommendations lists the owner's records, optionally for one scope.
func (s *PostgresAuthority) ListRecommendations(ctx context.Context, tenantID, ownerID, scope string, now time.Time) ([]Recommendation, error) {
	out := []Recommendation{}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := expireScope(ctx, tx, tenantID, ownerID, scope, now.UTC()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND card_kind='layout-recommendation' AND ($3='' OR recommendation_scope=$3)
			ORDER BY created_at DESC LIMIT 50`, tenantID, ownerID, scope)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			record, err := scanRecommendation(rows)
			if err != nil {
				return err
			}
			out = append(out, *record)
		}
		return rows.Err()
	})
	return out, err
}

// Accept revalidates and accepts in one transaction. An equal decision on the
// fresh snapshot accepts under the shown digest; a material change supersedes
// the record and returns the replacement to ask again.
func (s *PostgresAuthority) Accept(ctx context.Context, request AcceptRecommendation) (AcceptOutcome, error) {
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var outcome AcceptOutcome
	err := s.withTenant(ctx, request.TenantID, func(tx *sql.Tx) error {
		if err := expireScope(ctx, tx, request.TenantID, request.OwnerSubjectID, "", now); err != nil {
			return err
		}
		current, err := scanRecommendation(tx.QueryRowContext(ctx, `SELECT `+recommendationColumns+` FROM ril_action_cards
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='layout-recommendation' FOR UPDATE`,
			request.TenantID, request.OwnerSubjectID, request.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRecommendationNotFound
		}
		if err != nil {
			return err
		}
		switch decideAcceptance(*current, request, now) {
		case decideRefuseDigest:
			return ErrRecommendationDigest
		case decideRefuseState:
			return ErrRecommendationState
		case decideReplay:
			outcome = AcceptOutcome{Accepted: current, Replayed: true}
			return nil
		case decideSupersede:
			fresh := request.Fresh
			fresh.TenantID, fresh.OwnerSubjectID, fresh.Scope = current.TenantID, current.OwnerSubjectID, current.Scope
			if err := validateProposal(fresh); err != nil {
				return err
			}
			replacementID := newRecommendationID()
			superseded, err := scanRecommendation(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status='superseded', superseded_by=$4, updated_at=now()
				WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 RETURNING `+recommendationColumns,
				request.TenantID, request.OwnerSubjectID, current.ID, replacementID))
			if err != nil {
				return err
			}
			if err := supersedeScope(ctx, tx, fresh, replacementID, now); err != nil {
				return err
			}
			replacement, err := insertRecommendation(ctx, tx, replacementID, fresh, now, freshValidity(fresh))
			if err != nil {
				return err
			}
			if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
				TenantID: request.TenantID, CardID: current.ID, FromStatus: current.State, ToStatus: RecommendationSuperseded,
				CorrelationID: newAuditCorrelation(request.CorrelationID), ActorSubjectID: request.OwnerSubjectID, OccurredAt: now,
			}); err != nil {
				return err
			}
			outcome = AcceptOutcome{Superseded: superseded, Replacement: replacement}
			return nil
		}
		correlation := newAuditCorrelation(request.CorrelationID)
		acceptance := Acceptance{
			ReceiptRef: "recommendation-acceptance:" + current.ID, ActorSubjectID: request.OwnerSubjectID,
			AuditCorrelation: correlation, AcceptedAt: now,
			RevalidatedSnapshotDigest: request.Fresh.SnapshotDigest, RevalidatedProposalDigest: request.Fresh.ProposalDigest,
			RevalidatedMaterialMatched: true,
		}
		acceptance.ReceiptHash = digestText(acceptance.ReceiptRef + "\x00" + current.ProposalDigest + "\x00" + request.Fresh.SnapshotDigest + "\x00" + request.OwnerSubjectID + "\x00" + correlation + "\x00" + now.Format(time.RFC3339Nano))
		data, err := json.Marshal(acceptance)
		if err != nil {
			return err
		}
		accepted, err := scanRecommendation(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status='accepted', approval_json=$4::jsonb, approved_at=$5,
				idempotency_key=$6, snoozed_until=NULL, updated_at=now()
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND status IN ('open','snoozed') RETURNING `+recommendationColumns,
			request.TenantID, request.OwnerSubjectID, current.ID, string(data), now, "recommendation:"+current.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRecommendationState
		}
		if err != nil {
			return err
		}
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: request.TenantID, CardID: current.ID, FromStatus: current.State, ToStatus: RecommendationAccepted,
			CorrelationID: correlation, ActorSubjectID: request.OwnerSubjectID, OccurredAt: now,
		}); err != nil {
			return err
		}
		outcome = AcceptOutcome{Accepted: accepted}
		return nil
	})
	return outcome, err
}

func freshValidity(fresh ProposeRecommendation) time.Duration {
	if fresh.Validity > 0 {
		return fresh.Validity
	}
	return DefaultRecommendationValidity
}

// Reject records the owner's no. Equivalent advice stays suppressed until a
// materially different decision arrives (Propose matches the material digest).
func (s *PostgresAuthority) Reject(ctx context.Context, tenantID, ownerID, id, correlationID string, now time.Time) (*Recommendation, error) {
	return s.recommendationTransition(ctx, tenantID, ownerID, id, `status IN ('open','snoozed')`, RecommendationRejected, `denied_at=$5, snoozed_until=NULL`, now, correlationID, now)
}

// Snooze hides the advice until an explicit time.
func (s *PostgresAuthority) Snooze(ctx context.Context, tenantID, ownerID, id string, until time.Time, correlationID string, now time.Time) (*Recommendation, error) {
	if !until.After(now) || until.Sub(now) > maxSnooze {
		return nil, fmt.Errorf("recommendation: snooze must end within %s", maxSnooze)
	}
	return s.recommendationTransition(ctx, tenantID, ownerID, id, `status IN ('open','snoozed')`, RecommendationSnoozed, `snoozed_until=$5`, until.UTC(), correlationID, now)
}

// MarkExecuting binds the accepted advice to the job that carries it out.
func (s *PostgresAuthority) MarkExecuting(ctx context.Context, tenantID, ownerID, id, executionID string, now time.Time) (*Recommendation, error) {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil, fmt.Errorf("recommendation: execution id required")
	}
	return s.recommendationTransition(ctx, tenantID, ownerID, id, `status='accepted'`, RecommendationExecuting, `execution_id=$5, execution_started_at=now()`, executionID, "", now)
}

// Finish records the end of the execution.
func (s *PostgresAuthority) Finish(ctx context.Context, tenantID, ownerID, id string, succeeded bool, errorCode string, now time.Time) (*Recommendation, error) {
	to := RecommendationDone
	if !succeeded {
		to = RecommendationFailed
	}
	return s.recommendationTransition(ctx, tenantID, ownerID, id, `status='executing'`, to, `error_code=NULLIF($5,''), completed_at=now()`, strings.TrimSpace(errorCode), "", now)
}

func (s *PostgresAuthority) recommendationTransition(ctx context.Context, tenantID, ownerID, id, statusPredicate, to, set string, value any, correlationID string, now time.Time) (*Recommendation, error) {
	var out *Recommendation
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := expireScope(ctx, tx, tenantID, ownerID, "", now.UTC()); err != nil {
			return err
		}
		var from string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='layout-recommendation' FOR UPDATE`, tenantID, ownerID, id).Scan(&from); errors.Is(err, sql.ErrNoRows) {
			return ErrRecommendationNotFound
		} else if err != nil {
			return err
		}
		record, err := scanRecommendation(tx.QueryRowContext(ctx, `UPDATE ril_action_cards SET status=$4, `+set+`, updated_at=now()
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='layout-recommendation' AND `+statusPredicate+` RETURNING `+recommendationColumns,
			tenantID, ownerID, id, to, value))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRecommendationState
		}
		if err != nil {
			return err
		}
		executionID := ""
		if to == RecommendationExecuting {
			executionID, _ = value.(string)
		}
		if err := appendActionTransitionAudit(ctx, tx, actionTransitionAudit{
			TenantID: tenantID, CardID: id, FromStatus: from, ToStatus: to, ExecutionID: executionID,
			CorrelationID: newAuditCorrelation(correlationID), ActorSubjectID: ownerID, OccurredAt: now,
		}); err != nil {
			return err
		}
		out = record
		return nil
	})
	return out, err
}

var _ RecommendationAuthority = (*PostgresAuthority)(nil)
