package stacks

import (
	"sync"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

type ControlPlaneStores struct {
	Stacks     controlplane.StackStore
	Jobs       controlplane.JobStore
	Wallet     controlplane.WalletStore
	Servers    controlplane.ServerRuntimeStore
	Routing    stackrouting.Store
	Homelabs   controlplane.HomelabStore
	WizardRuns controlplane.WizardRunStore
}

var controlPlaneStores struct {
	mu     sync.RWMutex
	stores ControlPlaneStores
}

func ConfigureControlPlaneStores(stores ControlPlaneStores) {
	controlPlaneStores.mu.Lock()
	defer controlPlaneStores.mu.Unlock()
	controlPlaneStores.stores = stores
}

func currentControlPlaneStores() ControlPlaneStores {
	controlPlaneStores.mu.RLock()
	defer controlPlaneStores.mu.RUnlock()
	return controlPlaneStores.stores
}

var managedRuntimeLeases struct {
	mu     sync.RWMutex
	lister monthlyruntime.LeaseLister
}

// ConfigureManagedRuntimeLeaseLister late-binds the lease inventory used by
// managed-runtime routing and visibility. Capacity authority lives exclusively
// in atomic native provider admission and must not be inferred from this view.
func ConfigureManagedRuntimeLeaseLister(lister monthlyruntime.LeaseLister) {
	managedRuntimeLeases.mu.Lock()
	defer managedRuntimeLeases.mu.Unlock()
	managedRuntimeLeases.lister = lister
}

func currentManagedRuntimeLeaseLister() monthlyruntime.LeaseLister {
	managedRuntimeLeases.mu.RLock()
	defer managedRuntimeLeases.mu.RUnlock()
	return managedRuntimeLeases.lister
}
