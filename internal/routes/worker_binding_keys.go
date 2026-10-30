package routes

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// WorkerBindingSigner keeps the established Guard signing contract independent
// of whether its existing per-worker key is held locally or by native custody.
type WorkerBindingSigner interface {
	PublicKeys() []guardbinding.PinnedKey
	Sign(*guardbinding.Binding) error
	SignLocalGrant(*guardbinding.LocalRepairGrant) error
}

type WorkerBindingSignerFactory func(context.Context, string, string) (WorkerBindingSigner, error)

// bindingSignerFor returns the signer for one worker's command bindings, derived
// from the control plane's base secret. Without one, bindings stay unsigned and
// newly paired Guards stay unpinned.
func (h workerRouteHandlers) bindingSignerFor(ctx context.Context, tenantID, workerID string) (WorkerBindingSigner, error) {
	if h.bindingSigner != nil {
		signer, err := h.bindingSigner(ctx, tenantID, workerID)
		if err != nil {
			return nil, err
		}
		if signer == nil || len(signer.PublicKeys()) == 0 {
			return nil, fmt.Errorf("native Guard binding signer returned no public key")
		}
		return signer, nil
	}
	if len(h.bindingKeyBase) == 0 {
		return nil, nil
	}
	return guardbinding.DeriveWorkerSigner(h.bindingKeyBase, tenantID, workerID)
}
