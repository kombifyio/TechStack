package actions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
	"strings"
	"time"
)

type DecisionRequester struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type DecisionProposal struct {
	OperationID        string            `json:"operationId"`
	Requester          DecisionRequester `json:"requester"`
	Effect             map[string]any    `json:"effect"`
	EffectDigest       string            `json:"effectDigest"`
	LifecycleRevision  int64             `json:"lifecycleRevision"`
	LifecycleExpiresAt int64             `json:"lifecycleExpiresAt"`
	ServerRevision     int64             `json:"serverRevision"`
}

// PrepareDecision freezes a server-derived effect and current lifecycle heads before an owner decision.
func (s *PostgresAuthority) PrepareDecision(ctx context.Context, tenant, owner, cardID string, requester DecisionRequester) (*DecisionProposal, error) {
	if tenant == "" || owner == "" || cardID == "" || requester.ID == "" || (requester.Kind != "service" && requester.Kind != "agent_instance") {
		return nil, ErrExecutionAdmission
	}
	var result *DecisionProposal
	err := s.withTenant(ctx, tenant, func(tx *sql.Tx) error {
		card, err := scanGovernedCard(tx.QueryRowContext(ctx, `SELECT `+governedCardColumns+` FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND card_kind='governed-action' FOR UPDATE`, tenant, owner, cardID))
		if err != nil {
			return err
		}
		if card.Status != "awaiting_approval" || card.Template.Primitive.ID != "verify-stackkit-state" || card.Template.Primitive.OperationClass != "product-verify" || len(card.Template.Inputs) != 0 {
			return ErrExecutionAdmission
		}
		var revision, serverRevision, leaseRevision int64
		var stored string
		var leaseExpires time.Time
		err = tx.QueryRowContext(ctx, `SELECT c.decision_card_revision, COALESCE(c.decision_proposal_json::text,''), s.revision, l.lease_revision, l.valid_until FROM ril_action_cards c JOIN servers s ON s.tenant_id=c.tenant_id AND s.id=c.server_id AND s.owner_subject_id=c.owner_subject_id AND s.stack_id=c.stack_id JOIN techstack_vm_leases l ON l.tenant_id=s.tenant_id AND l.id=s.lease_id AND l.server_id=s.id AND l.owner_subject_id=c.owner_subject_id WHERE c.tenant_id=$1 AND c.owner_subject_id=$2 AND c.id=$3 AND s.lifecycle_state='active' AND s.desired_state='running' AND s.connection_state='connected' AND l.desired_state='running' AND l.cancelled_at IS NULL AND l.valid_from<=now() AND l.valid_until>now() FOR UPDATE OF s,l`, tenant, owner, cardID).Scan(&revision, &stored, &serverRevision, &leaseRevision, &leaseExpires)
		if err != nil || revision < 1 || serverRevision < 1 || leaseRevision < 1 {
			return fmt.Errorf("%w: current lifecycle unavailable", ErrExecutionAdmission)
		}
		if stored != "" {
			var existing DecisionProposal
			if json.Unmarshal([]byte(stored), &existing) != nil || existing.Requester != requester || existing.LifecycleRevision != leaseRevision || existing.ServerRevision != serverRevision || existing.LifecycleExpiresAt > leaseExpires.Unix() {
				return ErrCardConflict
			}
			result = &existing
			return nil
		}
		if card.Status != "awaiting_approval" {
			return ErrExecutionAdmission
		}
		targetRaw, err := json.Marshal(card.Template.Target)
		if err != nil {
			return err
		}
		var source map[string]any
		if err = json.Unmarshal(targetRaw, &source); err != nil {
			return err
		}
		target := map[string]any{}
		for from, to := range map[string]string{"scope": "scope", "site_ref": "siteRef", "node_ref": "nodeRef", "module_instance_ref": "moduleInstanceRef", "runtime_instance_ref": "runtimeInstanceRef", "execution_channel_ref": "executionChannelRef"} {
			if v, ok := source[from]; ok {
				target[to] = v
			}
		}
		op := "op_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		effect := map[string]any{"schemaVersion": "kombify.ril-effect.v1", "operationId": op, "audience": "stackkits", "tenantId": tenant, "ownerSubject": owner, "cardId": card.ID, "cardRevision": revision, "serverId": card.ServerID, "stackId": card.Template.StackID, "primitive": map[string]any{"id": card.Template.Primitive.ID, "contractHash": card.Template.Primitive.ContractHash, "operationClass": card.Template.Primitive.OperationClass}, "resolvedPlanHash": card.Template.ResolvedPlanHash, "target": target, "inputs": []any{}}
		raw, err := json.Marshal(effect)
		if err != nil {
			return err
		}
		canonical, err := jsoncanonicalizer.Transform(raw)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(append(append([]byte("kombify.ril-effect.v1\n"), canonical...), '\n'))
		proposal := DecisionProposal{OperationID: op, Requester: requester, Effect: effect, EffectDigest: "sha256:" + hex.EncodeToString(digest[:]), LifecycleRevision: leaseRevision, LifecycleExpiresAt: leaseExpires.Unix(), ServerRevision: serverRevision}
		raw, err = json.Marshal(proposal)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE ril_action_cards SET decision_proposal_json=$4::jsonb WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 AND decision_proposal_json IS NULL`, tenant, owner, cardID, raw)
		if err != nil {
			return err
		}
		result = &proposal
		return nil
	})
	return result, err
}
