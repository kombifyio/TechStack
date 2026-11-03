package actions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// FrozenDecisionExecution contains only the identity of a previously admitted
// execution. It is never constructed from Gateway request parameters.
type FrozenDecisionExecution struct {
	ExecutionID    string `json:"executionId"`
	TraceID        string `json:"traceId"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ReadFrozenDecision returns a single row-locked snapshot of an admitted or
// terminal action. It never refreshes proposal, lifecycle, grant, or decision.
func (s *PostgresAuthority) ReadFrozenDecision(ctx context.Context, tenant, owner, cardID string, requester DecisionRequester) (*GovernedCard, *DecisionProposal, *FrozenDecisionExecution, error) {
	if tenant == "" || owner == "" || cardID == "" || requester.ID == "" || (requester.Kind != "service" && requester.Kind != "agent_instance") {
		return nil, nil, nil, ErrExecutionAdmission
	}
	var card *GovernedCard
	var proposal DecisionProposal
	var execution FrozenDecisionExecution
	err := s.withTenant(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		card, err = scanGovernedCard(tx.QueryRowContext(ctx, `SELECT `+governedCardColumns+` FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='governed-action' FOR SHARE`, tenant, owner, cardID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCardNotFound
		}
		if err != nil {
			return err
		}
		switch card.Status {
		case "executing", "verifying", "completed", "failed":
		default:
			return ErrExecutionAdmission
		}
		if card.ExecutionAdmission == nil || card.ExecutionID == "" || card.TraceID == "" || card.IdempotencyKey == "" {
			return ErrExecutionAdmission
		}
		var proposalJSON, requestJSON string
		var revision int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(decision_proposal_json::text,''),COALESCE(execution_request_json::text,''),decision_card_revision FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3`, tenant, owner, cardID).Scan(&proposalJSON, &requestJSON, &revision); err != nil {
			return err
		}
		var request rilaction.Request
		if proposalJSON == "" || requestJSON == "" || json.Unmarshal([]byte(proposalJSON), &proposal) != nil || json.Unmarshal([]byte(requestJSON), &request) != nil || proposal.Requester != requester || revision < 1 {
			return ErrExecutionAdmission
		}
		if err = rilaction.ValidateRequestShape(request); err != nil {
			return ErrExecutionAdmission
		}
		var effect struct {
			CardRevision int64  `json:"cardRevision"`
			CardID       string `json:"cardId"`
			TenantID     string `json:"tenantId"`
			OwnerSubject string `json:"ownerSubject"`
			ServerID     string `json:"serverId"`
			StackID      string `json:"stackId"`
			OperationID  string `json:"operationId"`
		}
		raw, err := json.Marshal(proposal.Effect)
		if err != nil || json.Unmarshal(raw, &effect) != nil {
			return ErrExecutionAdmission
		}
		canonical, err := canonicalEffectDigest(raw)
		if err != nil || canonical != proposal.EffectDigest {
			return ErrExecutionAdmission
		}
		if effect.CardRevision != revision || effect.CardID != cardID || effect.TenantID != tenant || effect.OwnerSubject != owner || effect.ServerID != card.ServerID || effect.StackID != card.Template.StackID || effect.OperationID != proposal.OperationID || request.ActionCardID != cardID || request.TenantID != tenant || request.StackID != card.Template.StackID || request.Primitive != card.Template.Primitive || request.Primitive.ID != "verify-stackkit-state" || request.ResolvedPlanHash != card.Template.ResolvedPlanHash || request.Target != card.Template.Target || proposal.ServerRevision != card.ExecutionAdmission.ServerRevision || proposal.LifecycleRevision != int64(card.ExecutionAdmission.LeaseRevision) || request.ExecutionID != card.ExecutionID || request.TraceID != card.TraceID || request.IdempotencyKey != card.IdempotencyKey {
			return fmt.Errorf("%w: frozen execution differs", ErrExecutionAdmission)
		}
		execution = FrozenDecisionExecution{ExecutionID: card.ExecutionID, TraceID: card.TraceID, IdempotencyKey: card.IdempotencyKey}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return card, &proposal, &execution, nil
}

func canonicalEffectDigest(raw []byte) (string, error) {
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte("kombify.ril-effect.v1\n"), canonical...), '\n'))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
