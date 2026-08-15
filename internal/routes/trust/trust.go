// Package trust provides HTTP routes for worker-enrollment pairing tokens.
package trust

import (
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

type RouteStores struct {
	Stacks  controlplane.StackStore
	Workers controlplane.WorkerStore
	Jobs    controlplane.JobStore
}

func RegisterRoutesWithStores(r *httpx.Router, app core.App, stores RouteStores) { // pocketbase-migration-compat: legacy app bridge while trust stores are wired
	RegisterPairingRoutesWithStores(r, app, stores)
}
