// Package hooks contains PocketBase lifecycle hooks for business logic.
package hooks

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/tenant"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterStackHooks registers all hooks related to stack lifecycle.
// The tenant enforcer stamps tenant_id on new records in SaaS mode.
func RegisterStackHooks(app core.App, te *tenant.Enforcer) {
	// ===========================================
	// STACK HOOKS
	// ===========================================

	// Auto-set owner_id on stack creation (HTTP request)
	app.OnRecordCreateRequest("stacks").BindFunc(func(e *core.RecordRequestEvent) error {
		// Set owner_id from authenticated user
		if e.Record.Get("owner_id") == "" {
			info, err := e.RequestInfo()
			if err == nil && info != nil && info.Auth != nil {
				e.Record.Set("owner_id", info.Auth.Id)
			}
		}

		// Stamp tenant_id in SaaS mode
		if te != nil {
			te.StampRecord(e.RequestEvent, e.Record)
		}

		// Set default status if not provided
		if e.Record.Get("status") == "" {
			e.Record.Set("status", "draft")
		}

		return e.Next()
	})

	// Log stack creation
	app.OnRecordAfterCreateSuccess("stacks").BindFunc(func(e *core.RecordEvent) error {
		ownerId := getStringField(e.Record, "owner_id")
		name := getStringField(e.Record, "name")
		logActivity(app, "stack_created", e.Record.Id, ownerId,
			fmt.Sprintf("Stack '%s' created", name))
		return e.Next()
	})

	// Log stack update
	app.OnRecordAfterUpdateSuccess("stacks").BindFunc(func(e *core.RecordEvent) error {
		ownerId := getStringField(e.Record, "owner_id")
		name := getStringField(e.Record, "name")
		logActivity(app, "stack_updated", e.Record.Id, ownerId,
			fmt.Sprintf("Stack '%s' updated", name))
		return e.Next()
	})

	// Cleanup on stack deletion (before delete)
	app.OnRecordDeleteRequest("stacks").BindFunc(func(e *core.RecordRequestEvent) error {
		stackId := e.Record.Id

		// Delete related nodes
		nodes, err := app.FindRecordsByFilter("nodes", "stack_id = {:stackId}", "", 0, 0,
			map[string]any{"stackId": stackId})
		if err == nil {
			for _, node := range nodes {
				// Delete services for this node first
				services, _ := app.FindRecordsByFilter("services", "node_id = {:nodeId}", "", 0, 0,
					map[string]any{"nodeId": node.Id})
				for _, svc := range services {
					_ = app.Delete(svc)
				}
				_ = app.Delete(node)
			}
		}

		// Delete related jobs
		jobs, err := app.FindRecordsByFilter("jobs", "stack_id = {:stackId}", "", 0, 0,
			map[string]any{"stackId": stackId})
		if err == nil {
			for _, job := range jobs {
				_ = app.Delete(job)
			}
		}

		// Delete activity log entries for this stack. Owner-level resources like
		// workers and pairing tokens are intentionally preserved because one owner
		// can manage multiple stacks.
		activities, err := app.FindRecordsByFilter("activity_log", "stack_id = {:stackId}", "", 0, 0,
			map[string]any{"stackId": stackId})
		if err == nil {
			for _, a := range activities {
				_ = app.Delete(a)
			}
		}

		return e.Next()
	})

	// Log stack deletion
	app.OnRecordAfterDeleteSuccess("stacks").BindFunc(func(e *core.RecordEvent) error {
		ownerId := getStringField(e.Record, "owner_id")
		name := getStringField(e.Record, "name")
		logActivity(app, "stack_deleted", e.Record.Id, ownerId,
			fmt.Sprintf("Stack '%s' deleted", name))
		return e.Next()
	})

	// ===========================================
	// NODE HOOKS
	// ===========================================

	// Stamp tenant_id on node creation
	app.OnRecordCreateRequest("nodes").BindFunc(func(e *core.RecordRequestEvent) error {
		if te != nil {
			te.StampRecord(e.RequestEvent, e.Record)
		}
		return e.Next()
	})

	// Log node creation
	app.OnRecordAfterCreateSuccess("nodes").BindFunc(func(e *core.RecordEvent) error {
		stackId := getStringField(e.Record, "stack_id")
		name := getStringField(e.Record, "name")
		role := getStringField(e.Record, "role")
		logActivity(app, "node_added", stackId, "",
			fmt.Sprintf("Node '%s' added (role=%s)", name, role))
		return e.Next()
	})

	// Log node deletion
	app.OnRecordAfterDeleteSuccess("nodes").BindFunc(func(e *core.RecordEvent) error {
		stackId := getStringField(e.Record, "stack_id")
		name := getStringField(e.Record, "name")
		logActivity(app, "node_removed", stackId, "",
			fmt.Sprintf("Node '%s' removed", name))
		return e.Next()
	})

	// ===========================================
	// JOB HOOKS
	// ===========================================

	// Update stack status based on job state
	app.OnRecordAfterUpdateSuccess("jobs").BindFunc(func(e *core.RecordEvent) error {
		jobType := getStringField(e.Record, "type")
		jobState := getStringField(e.Record, "state")
		stackId := getStringField(e.Record, "stack_id")

		if stackId == "" {
			return e.Next()
		}

		stack, err := app.FindRecordById("stacks", stackId)
		if err != nil {
			return e.Next()
		}

		// Map job state to stack status
		var newStatus string
		switch jobState {
		case "running":
			switch jobType {
			case "provision":
				newStatus = "provisioning"
			case "destroy":
				newStatus = "stopping"
			}
		case "completed":
			switch jobType {
			case "provision":
				newStatus = "running"
				logActivity(app, "provision_completed", stackId, "",
					fmt.Sprintf("Provisioning completed for stack '%s'", stack.Get("name")))
			case "destroy":
				newStatus = "stopped"
			}
		case "failed":
			newStatus = "error"
			errMsg := getStringField(e.Record, "error_message")
			logActivity(app, "provision_failed", stackId, "",
				fmt.Sprintf("Provisioning failed for stack '%s': %s", stack.Get("name"), errMsg))
		}

		if newStatus != "" && stack.Get("status") != newStatus {
			stack.Set("status", newStatus)
			saveOrWarn(app, stack, "failed to persist stack hook update")
		}

		return e.Next()
	})

	// Stamp tenant_id on job creation (propagate from stack)
	app.OnRecordCreateRequest("jobs").BindFunc(func(e *core.RecordRequestEvent) error {
		if te != nil {
			te.StampRecord(e.RequestEvent, e.Record)
		}
		return e.Next()
	})

	// Log job creation (provision/destroy start)
	app.OnRecordAfterCreateSuccess("jobs").BindFunc(func(e *core.RecordEvent) error {
		jobType := getStringField(e.Record, "type")
		stackId := getStringField(e.Record, "stack_id")

		if jobType == "provision" {
			logActivity(app, "provision_started", stackId, "",
				"Provisioning started")
		}

		return e.Next()
	})

	// ===========================================
	// SERVICE HOOKS
	// ===========================================

	// Stamp tenant_id on service creation
	app.OnRecordCreateRequest("services").BindFunc(func(e *core.RecordRequestEvent) error {
		if te != nil {
			te.StampRecord(e.RequestEvent, e.Record)
		}
		return e.Next()
	})

	// Log service status changes
	app.OnRecordAfterUpdateSuccess("services").BindFunc(func(e *core.RecordEvent) error {
		status := getStringField(e.Record, "status")
		name := getStringField(e.Record, "name")
		nodeId := getStringField(e.Record, "node_id")

		// Get stack_id from node
		stackId := ""
		if nodeId != "" {
			if node, err := app.FindRecordById("nodes", nodeId); err == nil {
				stackId = getStringField(node, "stack_id")
			}
		}

		switch status {
		case "running":
			logActivity(app, "service_started", stackId, "",
				fmt.Sprintf("Service '%s' started", name))
		case "stopped":
			logActivity(app, "service_stopped", stackId, "",
				fmt.Sprintf("Service '%s' stopped", name))
		}

		return e.Next()
	})
}

// getStringField safely extracts a string field from a record
func getStringField(record *core.Record, field string) string {
	if v := record.Get(field); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// logActivity creates an entry in the activity_log collection
func logActivity(app core.App, action, stackId, userId, details string) {
	collection, err := app.FindCollectionByNameOrId("activity_log")
	if err != nil {
		fmt.Printf("⚠️  Could not find activity_log collection: %v\n", err)
		return
	}

	record := core.NewRecord(collection)
	record.Set("action", action)
	record.Set("details", details)
	if stackId != "" {
		record.Set("stack_id", stackId)
		if collection.Fields.GetByName("tenant_id") != nil {
			if stack, err := app.FindRecordById("stacks", stackId); err == nil {
				if tenantID := strings.TrimSpace(stack.GetString("tenant_id")); tenantID != "" {
					record.Set("tenant_id", tenantID)
				}
			}
		}
	}
	if userId != "" {
		record.Set("user_id", userId)
	}

	if err := app.Save(record); err != nil {
		fmt.Printf("⚠️  Could not save activity log: %v\n", err)
	}
}

// saveOrWarn persists model and logs a warning on failure. Keeping callers
// branch-free lets the hook-registration path stay within the cyclo budget.
func saveOrWarn(app core.App, model core.Model, msg string) {
	if err := app.Save(model); err != nil {
		app.Logger().Warn(msg, "error", err)
	}
}
