package providercontroljobs

import (
	"context"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func (m *Manager) VerifyCustomerGuest(ctx context.Context, tenant, owner, stack, lease, server, operation string) error {
	verifier, ok := m.admission.(interface {
		VerifyCustomerGuest(context.Context, string, string, string, string, string, string) error
	})
	if !ok {
		return providercontrol.ErrInvalidRequest
	}
	return verifier.VerifyCustomerGuest(ctx, tenant, owner, stack, lease, server, operation)
}

// AdmitSubstrateGuest reuses native admission without entering commercial
// product capacity. Placement and custody are checked again transactionally.
func (m *Manager) AdmitSubstrateGuest(ctx context.Context, request providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error) {
	if m == nil || m.admission == nil || request.Lease.CustodyClass != vmlease.CustodyCustomerSubstrate {
		return providercontrol.NativeProvisionAdmissionResult{}, providercontrol.ErrInvalidRequest
	}
	return m.admission.AdmitProvision(ctx, request)
}
