package main

import (
	"context"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"os"
)

func registerLocalHomeAssistantMigrations(router *httpx.Router, cfg *config.Config, deps routeDeps, manager jobs.ManagedLeaseManager) {
	routeConfig := routes.HomeAssistantMigrationRoutes{}
	path := os.Getenv("TECHSTACK_HOME_ASSISTANT_BINDINGS_FILE")
	if path != "" {
		if !cfg.DeploymentMode.IsSelfHosted() || deps.v2 == nil || deps.v2.db == nil {
			panic("Home Assistant native migration requires a local executor and durable database")
		}
		bindings, tenants, err := homeassistant.LoadLocalBindings(context.Background(), deps.v2.db.DB, path)
		if err != nil {
			panic("Home Assistant local binding initialization failed: " + err.Error())
		}
		admission, ok := manager.(substrateprovision.Admission)
		if !ok {
			panic("Home Assistant migration requires native customer substrate admission")
		}
		service := substrateprovision.New(deps.v2.db.DB, admission)
		for _, binding := range bindings {
			if runtime, ok := binding.Runtime.(*homeassistant.ProxmoxMigrationRuntime); ok {
				runtime.Provisioner = &homeAssistantSubstrateProvisioner{db: deps.v2.db.DB, service: service, request: binding.ProvisionRequest}
			}
		}
		if err := homeassistant.RegisterBindings(deps.workflowEngine, bindings); err != nil {
			panic(err)
		}
		routeConfig = routes.HomeAssistantMigrationRoutes{Engine: deps.workflowEngine, Bindings: bindings, Tenants: tenants}
	}
	routes.RegisterHomeAssistantMigrationRoutes(router, routeConfig)
	operations := routes.HomeAssistantOperationRoutes{Recovery: routeConfig.Bindings, RecoveryTenants: routeConfig.Tenants, Enrollments: homeAssistantEnrollmentStore(cfg, deps)}
	if ownerPath := os.Getenv("TECHSTACK_HOME_ASSISTANT_OWNERS_FILE"); ownerPath != "" {
		if !cfg.DeploymentMode.IsSelfHosted() || deps.v2 == nil || deps.v2.db == nil {
			panic("Home Assistant lifecycle requires local execution and durable custody")
		}
		owners, err := homeassistant.LoadLocalOwnerBindings(context.Background(), deps.v2.db.DB, ownerPath)
		if err != nil {
			panic("Home Assistant lifecycle initialization failed: " + err.Error())
		}
		operations.Bindings = owners
	}
	routes.RegisterHomeAssistantOperationRoutes(router, operations)
	routes.RegisterHomeAssistantEnrollmentRoutes(router, homeAssistantEnrollmentStore(cfg, deps))
}

func homeAssistantEnrollmentStore(cfg *config.Config, deps routeDeps) *homeassistant.EnrollmentStore {
	if !cfg.DeploymentMode.IsSelfHosted() || deps.v2 == nil || deps.v2.db == nil {
		return nil
	}
	store := &homeassistant.EnrollmentStore{DB: deps.v2.db.DB, Encryptor: auth.GetEncryptor()}
	store.VerifyFreshEndpoint = store.VerifyNativeGuestEndpoint
	return store
}

func localHomeAssistantOperations(cfg *config.Config, deps routeDeps, fallback routes.WorkerStackKitOperations) routes.WorkerStackKitOperations {
	path := os.Getenv("TECHSTACK_HOME_ASSISTANT_OWNERS_FILE")
	store := homeAssistantEnrollmentStore(cfg, deps)
	if path == "" && store == nil {
		return fallback
	}
	if !cfg.DeploymentMode.IsSelfHosted() || deps.v2 == nil || deps.v2.db == nil {
		panic("Home Assistant API owner requires local execution and durable custody")
	}
	var bindings []homeassistant.OwnerBinding
	if path != "" {
		var err error
		bindings, err = homeassistant.LoadLocalOwnerBindings(context.Background(), deps.v2.db.DB, path)
		if err != nil {
			panic("Home Assistant API owner initialization failed: " + err.Error())
		}
	}
	return &homeassistant.OwnerOperations{Bindings: bindings, Fallback: fallback, Enrollments: store}
}
