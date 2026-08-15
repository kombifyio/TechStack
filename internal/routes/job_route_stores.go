package routes

import (
	"sync"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

type JobRouteStores struct {
	Stacks controlplane.StackStore
	Jobs   controlplane.JobStore
}

var jobRouteStores struct {
	mu     sync.RWMutex
	stores JobRouteStores
}

func ConfigureJobRouteStores(stores JobRouteStores) {
	jobRouteStores.mu.Lock()
	defer jobRouteStores.mu.Unlock()
	jobRouteStores.stores = stores
}

func currentJobRouteStores() JobRouteStores {
	jobRouteStores.mu.RLock()
	defer jobRouteStores.mu.RUnlock()
	return jobRouteStores.stores
}
