package routes

import (
	"context"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// interruptedGuardWorker finds the worker an interrupted enrollment of the same
// host left behind on the token's stack. A Guard killed mid-redemption keeps its
// key, so its next pairing capability arrives with the key that was already
// issued a credential that never reached confirmation. That redemption replaces
// the unconfirmed credential in place instead of adding a second worker for one
// host: the earlier credential stops working because the worker's only valid
// credential becomes the new one.
//
// Only an identity that never confirmed (issued_unconfirmed: no request
// authenticated by a proof of the key ever arrived, so the worker never reported
// in) is replaced, and only within the capability's own tenant, stack and owner. A bound worker is never taken over: a different key, or the
// same key once the binding is confirmed, enrolls as an additional machine.
func (h workerRouteHandlers) interruptedGuardWorker(ctx context.Context, token controlplane.PairingToken, thumbprint string) *controlplane.Worker {
	if h.identityStore == nil || token.StackID == "" {
		return nil
	}
	identity, err := h.identityStore.GetUnconfirmedWorkerIdentityByKey(ctx, token.TenantID, token.StackID, thumbprint)
	if err != nil || identity == nil {
		// No match, or a lookup failure: the redemption proceeds as an
		// additional machine, never as a takeover.
		return nil
	}
	worker, err := h.wst.GetWorker(ctx, token.TenantID, identity.WorkerID)
	if err != nil || worker == nil ||
		worker.TenantID != token.TenantID || worker.StackID != token.StackID ||
		worker.OwnerSubjectID != token.OwnerSubjectID {
		return nil
	}
	return worker
}
