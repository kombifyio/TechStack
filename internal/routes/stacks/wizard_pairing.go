package stacks

import (
	"context"
	"errors"
	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/google/uuid"
	"strings"
	"time"
)

func (h wizardRunHandlers) prepareKeyedWizardPairing(ctx context.Context, run *wizardRunState, stack *controlplane.Stack, params trust.PairingTokenParams) (trust.MintedStackPairing, error) {
	store, ok := h.wizardRuns.(controlplane.WizardPairingPreparationStore)
	if !ok {
		return trust.MintedStackPairing{}, errors.New("keyed pairing preparation authority is unavailable")
	}
	kind := "update"
	if params.ServerProvisioningMode == "connect-remote" {
		kind = "remote_enrollment"
	}
	result := map[string]any{"spec_node_id": params.SpecNodeID, "server_provisioning_mode": params.ServerProvisioningMode}
	entry := h.wizardRunLedgerEntry(run, controlplane.WizardRun{StackID: stack.ID, NodeID: params.SpecNodeID, Status: "failed", ErrorReason: "pairing_preparation_pending"}, map[string]any{})
	job, prepared, err := store.PrepareWizardPairing(ctx, entry, controlplane.UpsertJobRequest{ID: uuid.NewString(), TenantID: run.tenantID, StackID: stack.ID, Type: kind, Result: result, Payload: map[string]any{"tenant_id": run.tenantID, "owner_id": run.ownerID, "remote_enrollment_recovery": true, "planned_server_id": run.plannedPairingServerID(stack, params.SpecNodeID)}}, func(workers controlplane.WorkerStore) (map[string]any, error) {
		stores := h.cfg.Trust
		stores.Workers = workers
		minted, e := trust.MintStackPairingTokenOnly(ctx, stores, run.tenantID, run.ownerID, stack, params)
		if e != nil {
			return nil, e
		}
		return trust.PairingJobResult(params, minted), nil
	})
	if err != nil {
		return trust.MintedStackPairing{}, err
	}
	run.pairingJobID = job.ID
	expires := time.Time{}
	switch v := job.Result["token_expires_at"].(type) {
	case time.Time:
		expires = v
	case string:
		expires, _ = time.Parse(time.RFC3339Nano, v)
	}
	token, _ := job.Result["registration_token"].(string)
	return trust.MintedStackPairing{JobID: job.ID, Token: strings.TrimSpace(token), ExpiresAt: expires, Reused: !prepared}, nil
}

func (run *wizardRunState) plannedPairingServerID(stack *controlplane.Stack, nodeRef string) string {
	return runtimeidentity.StackServerID(stack.ID, nodeRef)
}
