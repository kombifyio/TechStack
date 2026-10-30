package agentcontrol

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// PredecessorCommandsForTenant returns the prior commands, among those a Guard
// still holds, whose outcome the current worker may deliver: commands of the
// same tenant offered under the binding contract to another worker, still
// awaiting an outcome. The caller must have established that each prior worker
// served the same server. Nothing else is returned, so the recovery statement
// built from the answer cannot name a settled command, another tenant's
// command or the current worker's own.
func (h *Hub) PredecessorCommandsForTenant(ctx context.Context, tenantID, agentID string, held []guardbinding.PredecessorCommand) ([]guardbinding.PredecessorCommand, error) {
	tenantID, agentID = strings.TrimSpace(tenantID), strings.TrimSpace(agentID)
	if h == nil || h.db == nil || tenantID == "" || agentID == "" || len(held) == 0 {
		return nil, nil
	}
	if len(held) > guardbinding.MaxPredecessorCommands {
		held = held[:guardbinding.MaxPredecessorCommands]
	}
	ids := make([]string, 0, len(held))
	for _, c := range held {
		if id := strings.TrimSpace(c.CommandID); id != "" && strings.TrimSpace(c.WorkerID) != agentID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT command_id, agent_id
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id <> $2 AND command_family = 'stackkit'
		  AND contract_version = $3 AND worker_generation IS NOT NULL
		  -- An open outcome may get a new revision. A settled command with a
		  -- committed revision is listed too, so a recovered result whose
		  -- acknowledgement was lost is acknowledged again as an identical
		  -- duplicate; any other revision of it is refused at delivery.
		  AND (state IN ('dispatched', 'outcome_unknown') OR (state IN ('completed', 'failed') AND result_revision > 0))
		  AND command_id = ANY(SELECT jsonb_array_elements_text($4::jsonb))
		ORDER BY created_at
	`, tenantID, agentID, guardbinding.ContractVersion, string(encoded))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	claimed := make(map[string]string, len(held))
	for _, c := range held {
		claimed[strings.TrimSpace(c.CommandID)] = strings.TrimSpace(c.WorkerID)
	}
	var out []guardbinding.PredecessorCommand
	for rows.Next() {
		var c guardbinding.PredecessorCommand
		if err := rows.Scan(&c.CommandID, &c.WorkerID); err != nil {
			return nil, err
		}
		// The Guard's journal must name the same prior worker the control plane
		// offered the command to.
		if claimed[c.CommandID] == c.WorkerID {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}
