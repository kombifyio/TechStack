package executioncustody

import (
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// predecessorRecovery recomputes a result-only predecessor recovery statement
// from the custody provider's own records. Only commands that are still
// awaiting an outcome under the binding contract, offered to a revoked or
// rejected worker of the same tenant, owner and server, are kept; the issue
// time and lifetime are the provider's own.
func (p *Provider) predecessorRecovery(r *http.Request, grant CallerGrant, worker *controlplane.Worker, request *guardbinding.PredecessorRecovery) (*guardbinding.PredecessorRecovery, bool) {
	serverID := workerServerID(worker)
	if request.TenantID != grant.TenantID || request.WorkerID != worker.ID || serverID == "" || request.ServerID != serverID ||
		len(request.Commands) == 0 || len(request.Commands) > guardbinding.MaxPredecessorCommands {
		return nil, false
	}
	store := controlplane.NewPostgresStore(p.AuditDatabase)
	tx, err := db.BeginTenantTransaction(r.Context(), p.AuditDatabase, grant.TenantID)
	if err != nil {
		return nil, false
	}
	defer func() { _ = tx.Rollback() }()
	retired := map[string]bool{}
	var kept []guardbinding.PredecessorCommand
	for _, c := range request.Commands {
		if c.WorkerID == "" || c.WorkerID == worker.ID {
			return nil, false
		}
		ok, seen := retired[c.WorkerID]
		if !seen {
			prior, err := store.GetWorker(r.Context(), grant.TenantID, c.WorkerID)
			ok = err == nil && prior != nil && prior.TenantID == grant.TenantID && prior.OwnerSubjectID == grant.OwnerID &&
				workerServerID(prior) == serverID &&
				(strings.EqualFold(prior.Status, "revoked") || strings.EqualFold(prior.Status, "rejected"))
			retired[c.WorkerID] = ok
		}
		if !ok {
			continue
		}
		var open bool
		err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM typed_agent_commands WHERE tenant_id=$1 AND agent_id=$2 AND command_id=$3 AND command_family='stackkit' AND contract_version=$4 AND worker_generation IS NOT NULL AND (state IN ('dispatched','outcome_unknown') OR (state IN ('completed','failed') AND result_revision > 0)))`,
			grant.TenantID, c.WorkerID, c.CommandID, guardbinding.ContractVersion).Scan(&open)
		if err != nil {
			return nil, false
		}
		if open {
			kept = append(kept, guardbinding.PredecessorCommand{CommandID: c.CommandID, WorkerID: c.WorkerID})
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	canonical := guardbinding.NewPredecessorRecovery(grant.TenantID, worker.ID, serverID, kept, time.Now())
	return &canonical, true
}

func workerServerID(worker *controlplane.Worker) string {
	if worker == nil {
		return ""
	}
	serverID, _ := worker.Capabilities["server_id"].(string)
	return strings.TrimSpace(serverID)
}
