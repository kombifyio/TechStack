package executioncustody

import (
	"context"
	"errors"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
)

// scopeDeployment derives bootstrap scope from the existing product record;
// a configuration never invents the deployment id that Create will allocate.
func (p *Provider) scopeDeployment(ctx context.Context, grant CallerGrant, tenant, deployment, owner string) error {
	if tenant != grant.TenantID || owner == "" || owner != grant.OwnerID || !deploymentIDPattern.MatchString(deployment) {
		return errors.New("execution deployment scope denied")
	}
	if grant.DeploymentAdmission != "owned-records" && !grant.admits(tenant, deployment, owner) {
		return errors.New("execution deployment grant denied")
	}
	tx, err := db.BeginTenantTransaction(ctx, p.AuditDatabase, tenant)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recordedOwner string
	if err := tx.QueryRowContext(ctx, `SELECT owner_subject_id FROM stacks WHERE tenant_id=$1 AND id=$2`, tenant, deployment).Scan(&recordedOwner); err != nil || recordedOwner != owner {
		return errors.New("execution deployment owner is not recorded")
	}
	return nil
}

// verifyTrustImport reuses the canonical Guard result admission. The caller
// cannot choose the first local Owner: the completed bound command of this
// deployment's actual worker must already contain that exact import receipt.
func (p *Provider) verifyTrustImport(ctx context.Context, grant CallerGrant, binding TrustBinding) error {
	if p.scopeDeployment(ctx, grant, binding.TenantID, binding.DeploymentID, grant.OwnerID) != nil {
		return errors.New("execution trust deployment denied")
	}
	tx, err := db.BeginTenantTransaction(ctx, p.AuditDatabase, binding.TenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT c.command_json,c.result_json FROM typed_agent_commands c
 JOIN servers s ON s.tenant_id=c.tenant_id AND s.worker_id=c.agent_id
 WHERE c.tenant_id=$1 AND s.stack_id=$2 AND s.owner_subject_id=$3 AND c.state='completed'
 AND c.command_family='stackkit' AND c.contract_version=$4 AND c.result_generation>0
 AND c.result_generation=c.worker_generation AND c.result_revision>0 AND c.result_digest<>''
 ORDER BY c.completed_at DESC LIMIT 64`, binding.TenantID, binding.DeploymentID, grant.OwnerID, guardbinding.ContractVersion)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var commandJSON, resultJSON []byte
		if err := rows.Scan(&commandJSON, &resultJSON); err != nil {
			return err
		}
		command, result := &agentpb.StackKitCommand{}, &agentpb.StackKitResult{}
		if protojson.Unmarshal(commandJSON, command) != nil || protojson.Unmarshal(resultJSON, result) != nil || command.GetStackkitInstanceId() != binding.StackID {
			continue
		}
		if stackkitcommand.ValidateResult(result, command) != nil {
			continue
		}
		evidence, err := stackkitcommand.ParseAdvancedTrustImportEvidence(command, result)
		if err == nil && evidence.OwnerRef == binding.OwnerRef && evidence.BundleSHA256 == binding.BundleSHA256 {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return errors.New("authenticated Guard trust-import receipt is not recorded")
}
