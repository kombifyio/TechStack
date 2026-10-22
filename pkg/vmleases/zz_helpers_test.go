package vmleases

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"time"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func testLease(now time.Time) vmlease.Lease {
	return vmlease.Lease{
		ID:             "lease-1",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron-managed", EngineVMID: "ccloud-1", SimulationID: "sim-1", VMID: "vm-1"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyNever,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
	}
}

