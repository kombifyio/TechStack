// Package hooks contains PocketBase lifecycle hooks for business logic.
package hooks

import (
	"github.com/kombifyio/techstack/pkg/tenant"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterWorkerHooks registers hooks that enforce worker ownership server-side.
func RegisterWorkerHooks(app core.App, te *tenant.Enforcer) {
	// Enforce owner_id = request.auth.id on creation via the PocketBase CRUD API.
	app.OnRecordCreateRequest("workers").BindFunc(func(e *core.RecordRequestEvent) error {
		info, err := e.RequestInfo()
		if err == nil && info != nil && info.Auth != nil {
			e.Record.Set("owner_id", info.Auth.Id)
		}
		// Stamp tenant_id in SaaS mode
		if te != nil {
			te.StampRecord(e.RequestEvent, e.Record)
		}
		return e.Next()
	})

	// Prevent owner_id tampering on update.
	app.OnRecordUpdateRequest("workers").BindFunc(func(e *core.RecordRequestEvent) error {
		info, err := e.RequestInfo()
		if err == nil && info != nil && info.Auth != nil {
			e.Record.Set("owner_id", info.Auth.Id)
		}
		return e.Next()
	})
}
