package monthlyruntime

import (
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/demoguard"
)

// demoGuardForAction enforces the public-demo restrictions at the service
// layer (defense in depth below the route guards):
//   - protected anchor leases cannot be decommissioned by user-facing calls,
//     force included; internal control-plane calls (reset, reconciliation)
//     bypass via req.Internal.
//   - the demo tenant gets no SSH surface (enable-ssh / ssh info).
//
// With no demo environment configured every check is inert.
func demoGuardForAction(lease vmlease.Lease, req ActionRequest) error {
	if req.Internal {
		return nil
	}
	if req.Action == serverruntime.RuntimeActionDecommission && demoguard.IsProtectedLease(string(lease.ID)) {
		return ErrDecommissionBlockedProtected
	}
	switch req.Action {
	case serverruntime.RuntimeActionEnableSSH, serverruntime.RuntimeActionSSHInfo:
		if demoguard.IsDemoSubject(lease.Subject.OrgID, req.UserID) {
			return ErrDemoRestricted
		}
	}
	return nil
}
