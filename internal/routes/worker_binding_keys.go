package routes

import (
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// bindingSignerFor returns the signer for one worker's command bindings, derived
// from the control plane's base secret. Without one, bindings stay unsigned and
// newly paired Guards stay unpinned.
func (h workerRouteHandlers) bindingSignerFor(tenantID, workerID string) (*guardbinding.Signer, error) {
	if len(h.bindingKeyBase) == 0 {
		return nil, nil
	}
	return guardbinding.DeriveWorkerSigner(h.bindingKeyBase, tenantID, workerID)
}
