package main

import (
	"database/sql"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

// Hosted provider incident workflows are installed by a private extension
// file. Keeping this hook neutral lets the self-hosted export retain the
// durable workflow engine without claiming provider incident authority.
var registerHostedProviderWorkflows = func(*workflow.Engine, *sql.DB) {}

// bootWorkflowEngine constructs the RIL workflow engine over the Postgres store
// and registers the workflow definitions. It returns nil when Postgres is not
// configured (PocketBase-only deployments), in which case workflow-backed
// features stay dormant. Activity registration and the migrate trigger land with
// the registry wiring; the engine + worker are the durable substrate they need.
func bootWorkflowEngine(boot *v2Boot, log *logger.Logger) *workflow.Engine {
	if boot == nil || boot.db == nil {
		log.Info("ril_workflow_engine_disabled", "reason", "postgres_not_configured")
		return nil
	}
	engine := workflow.NewEngine(workflow.NewPgStore(boot.db.DB), workflow.NewActivityRunner())
	engine.Register(workflows.NewServiceMigrationWorkflow())
	registerHostedProviderWorkflows(engine, boot.db.DB)
	log.Info("ril_workflow_engine_ready", "store", "postgres")
	return engine
}
