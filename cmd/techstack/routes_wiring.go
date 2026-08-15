package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	commonauthlocal "github.com/kombifyio/go-common/authlocal"
	"github.com/kombifyio/go-common/authsession"

	"github.com/kombifyio/techstack/internal/breakglass"
	"github.com/kombifyio/techstack/internal/managedstackkit"
	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/rilactionexecution"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/routes/auth"
	stackroutes "github.com/kombifyio/techstack/internal/routes/stacks"
	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/agent/watchdog"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	storageauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/clientpairing"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	v2 "github.com/kombifyio/techstack/pkg/v2"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// Provider-control is a hosted extension. The neutral route wiring keeps the
// local inventory, StackKits, and auth surfaces intact while the private
// extension installs provider-only routes and error classification.
var (
	isProviderMutationActivationBlocked     = func(error) bool { return false }
	isProviderNativeDecommissionUnavailable = func(error) bool { return false }
	isProviderDecommissionPending           = func(*jobs.ManagedLeaseDecommissionResult, error) bool { return false }
	registerProviderIncidentRoutes          = func(*httpx.Router, *sql.DB) {}
	registerProviderResolutionRoutes        = func(*httpx.Router, any, routes.InventoryPolicy) {}
)

// managedLeaseRecoveryIntent hands decommission directly to the canonical
// provider-control application. A successful admission means either provider
// absence is already proven or a generation-bound provider operation is
// durable and discoverable by the provider-control reconciler. A generic wait
// or a closed mutation gate is not an admission receipt.
type managedLeaseRecoveryIntent struct {
	decommissioner jobs.ManagedLeaseDecommissioner
}

type managedLeaseDecommissionReadiness interface {
	ManagedLeaseDecommissionReady(context.Context) error
}

func (r managedLeaseRecoveryIntent) CheckProviderReconciliationReady(ctx context.Context) error {
	checker, ok := r.decommissioner.(managedLeaseDecommissionReadiness)
	if !ok || checker == nil {
		return monthlyruntime.ErrReconciliationUnavailable
	}
	if err := checker.ManagedLeaseDecommissionReady(ctx); err != nil {
		if isProviderMutationActivationBlocked(err) ||
			isProviderNativeDecommissionUnavailable(err) {
			return fmt.Errorf("%w: %v", monthlyruntime.ErrReconciliationUnavailable, err)
		}
		return err
	}
	return nil
}

func (r managedLeaseRecoveryIntent) EnqueueProviderReconciliation(ctx context.Context, req monthlyruntime.ReconciliationRequest) error {
	if !r.DurableReconciliationReady() {
		return monthlyruntime.ErrReconciliationUnavailable
	}
	if strings.TrimSpace(req.TenantID) == "" ||
		strings.TrimSpace(req.OwnerID) == "" || strings.TrimSpace(req.LeaseID) == "" ||
		strings.TrimSpace(req.ResourceGenerationDigest) == "" {
		return fmt.Errorf("managed lease recovery intent requires exact tenant, owner, lease, and generation")
	}
	result, err := r.decommissioner.DecommissionManagedLeases(ctx, jobs.ManagedLeaseDecommissionRequest{
		StackID: strings.TrimSpace(req.StackID), TenantID: strings.TrimSpace(req.TenantID),
		OwnerID: strings.TrimSpace(req.OwnerID), LeaseID: strings.TrimSpace(req.LeaseID),
		ResourceGenerationDigest: strings.TrimSpace(req.ResourceGenerationDigest),
	})
	if err == nil {
		if result == nil {
			return errors.New("managed lease decommission returned no provider result")
		}
		for _, proof := range result.Proofs {
			if strings.TrimSpace(proof.TenantID) == strings.TrimSpace(req.TenantID) &&
				(strings.TrimSpace(req.StackID) == "" || strings.TrimSpace(proof.StackID) == strings.TrimSpace(req.StackID)) &&
				strings.TrimSpace(proof.LeaseID) == strings.TrimSpace(req.LeaseID) &&
				strings.TrimSpace(proof.ResourceGenerationDigest) == strings.TrimSpace(req.ResourceGenerationDigest) &&
				(proof.ObservedState == jobs.ManagedLeaseDecommissionObservedDecommissioned ||
					proof.ObservedState == jobs.ManagedLeaseDecommissionObservedNotFound) {
				return nil
			}
		}
		return errors.New("managed lease decommission returned no exact terminal provider-absence proof")
	}
	if isProviderDecommissionPending(result, err) {
		return nil
	}
	if isProviderMutationActivationBlocked(err) ||
		isProviderNativeDecommissionUnavailable(err) {
		return fmt.Errorf("%w: %v", monthlyruntime.ErrReconciliationUnavailable, err)
	}
	return err
}

func (r managedLeaseRecoveryIntent) DurableReconciliationReady() bool {
	_, ok := r.decommissioner.(managedLeaseDecommissionReadiness)
	return r.decommissioner != nil && ok
}

func coreHealthDependencies(cfg *config.Config, grpcSrv *grpcserver.Server, database *db.DB) *routes.HealthDependencies {
	deps := &routes.HealthDependencies{
		Edition:        cfg.Edition,
		DeploymentMode: cfg.DeploymentMode,
		DB:             database,
	}
	// Production may deliberately disable the separate gRPC listener when the
	// supported HTTPS Guard transport is in use. Only a listener that was
	// actually configured and started is a critical readiness dependency.
	if grpcSrv != nil {
		deps.GRPCRunning = func() bool { return true }
	}
	return deps
}

func registerCoreRoutes(router *httpx.Router, deps routeDeps, managedLeases jobs.ManagedLeaseManager) {
	cfg := deps.startup.cfg
	grpcSrv := deps.grpc.server
	var database *db.DB
	if deps.v2 != nil {
		database = deps.v2.db
	}
	healthDeps := coreHealthDependencies(cfg, grpcSrv, database)
	routes.RegisterHealthRoutesWithDeps(router, deps.app, deps.version, deps.revision, deps.started, healthDeps)
	routes.RegisterInfoRoutes(router, deps.app, deps.version, deps.revision, deps.started, cfg.Edition, cfg.DeploymentMode, cfg.Server.Environment)
	routes.RegisterInstanceRoutes(router, deps.startup.identity, deps.version)
	clientProfile := techstackClientConnectionProfileConfig(cfg, deps.startup.identity)
	routes.RegisterClientConnectionProfileRoute(router, clientProfile)
	routes.RegisterClientPairingRoutes(router, techstackClientPairingRouteConfig(clientProfile, deps.v2))
	routes.RegisterEnrollmentRoutes(router, deps.startup.enrollmentResolver)
	routes.RegisterOpenAPIRoutes(router)
	routes.RegisterCapabilitiesRoutes(router, deps.version)
	routes.RegisterClientBootstrapRoutes(router, deps.version, cfg.Edition, cfg.DeploymentMode)
	routes.RegisterDocsRedirectRoutes(router)
	stores := stackControlPlaneStores(deps.v2)
	stackroutes.ConfigureControlPlaneStores(stores)
	routes.ConfigureJobRouteStores(routes.JobRouteStores{
		Stacks: stores.Stacks,
		Jobs:   stores.Jobs,
	})
	stackKitCommander := stackKitCommanderForDeployment(cfg.DeploymentMode, deps.typedControl, grpcSrv)
	deps.orch.ConfigureManagedStackKitInventory(managedStackKitInventoryBuilder(deps))
	deps.orch.ConfigureStackKitCommander(stackKitCommander)
	var notificationOutbox productnotifications.ProductEventEnqueuer
	if deps.monitor != nil {
		notificationOutbox = deps.monitor.notifyOutbox
	}
	stackroutes.RegisterRoutesWithModeAndFeatures(router, deps.app, deps.orch, cfg.DeploymentMode, deps.featureSvc, managedLeases, notificationOutbox)
	routes.RegisterDriftRoutes(router, deps.app, deps.orch)
	routes.RegisterJobsSSERoutes(router, deps.app)
	routes.RegisterDiscoveryRoutes(router, deps.app, nil)
	routes.RegisterPreCheckRoutes(router, deps.app, grpcSrv)
	routes.RegisterMetricsRoutes(router, deps.app)
	routes.RegisterBackupRoutes(router, deps.app, routes.BackupConfig{
		DataDir:     cfg.Server.DataDir,
		BackupDir:   cfg.Backup.BackupDir,
		Retention:   cfg.Backup.Retention,
		S3Enabled:   cfg.Backup.S3Enabled,
		S3Bucket:    cfg.Backup.S3Bucket,
		S3Endpoint:  cfg.Backup.S3Endpoint,
		S3AccessKey: cfg.Backup.S3AccessKey,
		S3SecretKey: cfg.Backup.S3SecretKey,
		S3Region:    cfg.Backup.S3Region,
		S3Prefix:    cfg.Backup.S3Prefix,
	})
	routes.RegisterAgentRoutes(router, deps.app, grpcSrv)
	routes.RegisterActivityRoutes(router, activityControlPlaneStore(deps.v2))
	routes.RegisterMonitoringRoutes(router, deps.app, deps.monitor.queryBackend, deps.monitor.status, grpcSrv)
	routes.RegisterAlertRoutes(router, deps.app, deps.monitor.alertEngine)
	routes.RegisterRegistryRoutesWithStores(router, deps.app, routes.RegistryRouteStores{
		Stacks:   stores.Stacks,
		Workers:  workerControlPlaneStore(deps.v2),
		Registry: registryControlPlaneStore(deps.v2),
		Jobs:     stores.Jobs,
		// Canonical serverregistry read model backs the legacy
		// /api/v1/registry/servers projection (kombify-Techstack-nzy1.4).
		Servers: serverRuntimeControlPlaneStore(deps.v2),
	})
	routes.RegisterSystemAccountRoutes(router, deps.app)
	routes.RegisterWalletRoutesWithConfig(router, deps.app, routes.WalletRouteConfig{
		Store:    walletControlPlaneStore(deps.v2),
		Activity: activityControlPlaneStore(deps.v2),
	})
	if err := routes.RegisterUnifierRoutes(router, deps.app); err != nil {
		fmt.Printf("⚠️  Unifier routes failed: %v\n", err)
	}
	trustWorkerStore := workerControlPlaneStore(deps.v2)
	if trustWorkerStore != nil {
		deps.log.Info("trust_routes_registered", "pairing_backend", "postgres")
	} else {
		deps.log.Warn("trust_routes_registered", "pairing_backend", "pocketbase")
	}
	routes.RegisterTrustRoutesWithStores(router, deps.app, routes.TrustRouteStores{
		Stacks:  stores.Stacks,
		Workers: trustWorkerStore,
		Jobs:    stores.Jobs,
	})
	routes.RegisterAuthRoutesWithConfig(router, deps.app, cfg.DeploymentMode, cfg.Edition, routes.AuthRouteConfig{
		PortalSession:         portalSession(deps.v2, cfg.IsProduction()),
		LocalSetupProvisioner: localSetupProvisioner(deps),
	})
	registerLocalDeviceSessionRoute(router, deps)
	routes.RegisterInternalRoutes(router, deps.app, cfg.DeploymentMode)
	routes.RegisterNotificationsRoutes(router)
	if deps.v2 != nil && deps.v2.db != nil {
		registerProviderIncidentRoutes(router, deps.v2.db.DB)
	}
}

// stackKitCommanderForDeployment keeps command dispatch on the transport the
// enrolled runtime actually polls. Hosted managed runtimes use the authenticated
// HTTPS Guard channel even when the process also exposes a gRPC server for
// other clients. Self-hosted installations retain their mTLS gRPC preference.
func stackKitCommanderForDeployment(mode config.DeploymentMode, https jobs.StackKitCommandSender, grpc jobs.StackKitCommandSender) jobs.StackKitCommandSender {
	if mode.IsSaaS() || grpc == nil {
		return https
	}
	return grpc
}

// portalSession builds the embedded-SSO session settings from the V2 boot so
// portal-verify can issue the techstack_session cookie the request path needs.
func portalSession(boot *v2Boot, secure bool) auth.PortalSession {
	if boot == nil || boot.session == nil {
		return auth.PortalSession{}
	}
	var authStore controlplane.AuthStore
	if boot.db != nil && boot.db.DB != nil {
		authStore = controlplane.NewPostgresStore(boot.db.DB)
	}
	return auth.PortalSession{
		Manager:       boot.session,
		CookieName:    boot.cookieName,
		DefaultTenant: boot.defaultTenant,
		Secure:        secure,
		AuthStore:     authStore,
	}
}

func stackControlPlaneStores(boot *v2Boot) stackroutes.ControlPlaneStores {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return stackroutes.ControlPlaneStores{}
	}
	store := controlplane.NewPostgresStore(boot.db.DB)
	return stackroutes.ControlPlaneStores{
		Stacks:     store,
		Jobs:       store,
		Wallet:     store,
		Servers:    store,
		Routing:    stackrouting.NewPostgresStore(boot.db.DB),
		Homelabs:   store,
		WizardRuns: store,
	}
}

func walletControlPlaneStore(boot *v2Boot) controlplane.WalletStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func activityControlPlaneStore(boot *v2Boot) controlplane.ActivityStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func workerControlPlaneStore(boot *v2Boot) controlplane.WorkerStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func clientPairingStore(boot *v2Boot) clientpairing.Store {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return clientpairing.NewPostgresStore(boot.db.DB)
}

func registryControlPlaneStore(boot *v2Boot) controlplane.RegistryStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func rilControlPlaneStore(boot *v2Boot) controlplane.RILStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func serverRuntimeControlPlaneStore(boot *v2Boot) controlplane.ServerRuntimeStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func serviceRuntimeControlPlaneStore(boot *v2Boot) controlplane.ServiceRuntimeStore {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	return controlplane.NewPostgresStore(boot.db.DB)
}

func registerRuntimeInventoryRoutes(router *httpx.Router, deps routeDeps, state runtimeRouteState, inventoryPolicy routes.InventoryPolicy) {
	registerProviderResolutionRoutes(router, deps.providerResolution, inventoryPolicy)
	var metricWriter routes.WorkerMetricWriter
	if deps.monitor != nil && deps.monitor.tsdb != nil {
		metricWriter = deps.monitor.tsdb
	}
	stackStores := stackControlPlaneStores(deps.v2)
	routes.RegisterStackLifecycleRoutesWithStores(router, deps.app, state.leaseSvc, nil, deps.featureSvc, routes.StackLifecycleStores{
		Stacks:         stackStores.Stacks,
		Workers:        workerControlPlaneStore(deps.v2),
		Jobs:           stackStores.Jobs,
		ManagedLeases:  state.runtimeActions.LeaseManager,
		Decommissioner: state.runtimeActions.LeaseDecommissioner,
	})
	workerStore := workerControlPlaneStore(deps.v2)
	if workerStore == nil {
		panic("registerRuntimeInventoryRoutes: DATABASE_URL required — PocketBase worker fallback removed (PB retirement)")
	}
	deps.log.Info("worker_routes_registered", "backend", "postgres")
	serverRuntimeStore := serverRuntimeControlPlaneStore(deps.v2)
	if serverRuntimeStore == nil {
		panic("registerRuntimeInventoryRoutes: DATABASE_URL required for canonical server registry")
	}
	routes.RegisterServerRuntimeRoutes(router, routes.ServerRuntimeRouteConfig{Store: serverRuntimeStore})
	serviceRuntimeStore := serviceRuntimeControlPlaneStore(deps.v2)
	if serviceRuntimeStore == nil {
		panic("registerRuntimeInventoryRoutes: DATABASE_URL required for canonical service registry")
	}
	routes.RegisterServiceRuntimeRoutes(router, routes.ServiceRuntimeRouteConfig{
		Store: serviceRuntimeStore, Stacks: stackStores.Stacks, Servers: serverRuntimeStore, Jobs: stackStores.Jobs, Orchestrator: deps.orch,
	})
	inventoryReadStore, ok := serverRuntimeStore.(controlplane.InventoryReadStore)
	if !ok {
		panic("registerRuntimeInventoryRoutes: canonical server store lacks authorization-scoped inventory reads")
	}
	routes.RegisterInventoryRoutes(router, routes.InventoryRouteConfig{
		ReadStore: inventoryReadStore,
		Policy:    inventoryPolicy,
		Version:   deps.version,
	})
	var stackKitOperations routes.WorkerStackKitOperations
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		deps.log.Warn("managed_stackkit_operations_unavailable", "reason", "database_not_configured")
	} else {
		custody, custodyErr := backupstore.NewPostgresCustodyStore(deps.v2.db.DB, storageauth.GetEncryptor())
		if custodyErr != nil {
			deps.log.Warn("managed_stackkit_operations_unavailable", "reason", "encrypted_custody_not_configured", "error", custodyErr)
		} else {
			operations, operationsErr := managedstackkit.NewOperations(custody)
			if operationsErr != nil {
				deps.log.Warn("managed_stackkit_operations_unavailable", "reason", "operations_not_configured", "error", operationsErr)
			} else {
				stackKitOperations = operations
			}
		}
	}
	routes.RegisterWorkerRoutesWithStore(router, routes.WorkerRouteConfig{
		MetricWriter:         metricWriter,
		ManagedRuntimeLeases: state.leaseSvc,
		Store:                workerStore,
		Servers:              serverRuntimeStore,
		Registry:             registryControlPlaneStore(deps.v2),
		RIL:                  rilControlPlaneStore(deps.v2),
		AgentIdentityIssuer:  workerAgentIdentityIssuer(deps),
		TypedControl:         deps.typedControl,
		StackKitOperations:   stackKitOperations,
		RuntimeLogs:          deps.grpc.server,
	})
	routes.RegisterStackOperationsRoutesWithStores(router, deps.app, deps.monitor.queryBackend, deps.monitor.status, deps.monitor.alertEngine, deps.grpc.server, routes.StackOperationsRouteStores{
		Stacks:   stackStores.Stacks,
		Servers:  serverRuntimeStore,
		Services: serviceRuntimeStore,
		Workers:  workerControlPlaneStore(deps.v2),
		Registry: registryControlPlaneStore(deps.v2),
		Jobs:     stackStores.Jobs,
	}, state.leaseSvc)
}

func workerAgentIdentityIssuer(deps routeDeps) routes.WorkerAgentIdentityIssuer {
	if deps.grpc == nil {
		return nil
	}
	return deps.grpc.identityIssuer
}

func registerVMLeaseRoutes(router *httpx.Router, deps routeDeps) (runtimeRouteState, error) {
	leaseStore, storeBackend := vmLeaseStore(deps.v2)
	// Simulate is not a managed-provider execution lane. Historical
	// legacy_simulate bindings are inventory-only quarantine records; no
	// enrollment or legacy provider client is wired here.
	// Native mutation stays fail-closed until provider-control admission can
	// atomically bind techstack_provider_control.
	deps.log.Info("vm_lease_legacy_simulate_execution_disabled")

	allowedProviders := map[string]bool{}
	providerSource := "blocked:no_active_catalog"
	if deps.startup.cfg.ProviderCatalog.Mode == config.ProviderCatalogSelfHostedStatic {
		allowedProviders = map[string]bool{"centron": true, "ionos": true}
		providerSource = string(config.ProviderCatalogSelfHostedStatic)
	} else if deps.v2 != nil && deps.v2.db != nil && deps.v2.db.DB != nil {
		catalogProviders, catalogErr := vmleases.LoadTechStackLeasingProviderSet(context.Background(), deps.v2.db.DB)
		if catalogErr != nil {
			deps.log.Warn("vm_lease_provider_catalog_unavailable", "error", catalogErr)
		} else if len(catalogProviders) == 0 {
			deps.log.Warn("vm_lease_provider_catalog_empty")
		} else {
			allowedProviders = catalogProviders
			providerSource = "provider_catalog"
		}
	}

	leaseSvc := vmleases.NewService(leaseStore, vmleases.ServiceConfig{
		AllowedProviders: allowedProviders,
	})
	deps.orch.ConfigureManagedRuntimeLeases(leaseSvc)

	// Provider-control admission is never staged through VMLeaseManagerAdapter.
	// The composed native actions remain fail-closed at their immutable
	// activation gate until contracts, runtime-role separation, and adapter
	// certification are complete.
	runtimeActions, diagnostics := jobs.RuntimeActionsFromEnv(deps.providerActions)
	for _, runner := range diagnostics.Configured {
		deps.log.Info("runtime_action_configured", "runner", runner)
	}
	for _, warning := range diagnostics.Warnings {
		deps.log.Warn("runtime_action_config_warning", "warning", warning)
	}
	deps.orch.ConfigureRuntimeActions(runtimeActions)
	routes.RegisterVMLeaseRoutes(router, vmleases.NewHandler(leaseSvc, vmleases.HandlerConfig{
		ServiceAuthSecret: os.Getenv("SERVICE_AUTH_SECRET"),
		ServiceAuthNext:   os.Getenv("SERVICE_AUTH_SECRET_NEXT"),
	}))
	deps.log.Info("vm_lease_routes_registered", "store", storeBackend, "provider_source", providerSource)
	return runtimeRouteState{leaseSvc: leaseSvc, runtimeActions: runtimeActions}, nil
}

func vmLeaseStore(boot *v2Boot) (vmleases.Store, string) {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		panic("vmLeaseStore: DATABASE_URL required — PocketBase fallback removed (PB retirement)")
	}
	return vmleases.NewPostgresStore(boot.db.DB), "postgres"
}

func registerRILRoutes(router *httpx.Router, deps routeDeps) {
	rilStore := rilControlPlaneStore(deps.v2)
	if rilStore == nil {
		panic("registerRILRoutes: DATABASE_URL required — PocketBase RIL inventory removed (PB retirement)")
	}
	authority := actions.NewPostgresAuthority(deps.v2.db.DB)
	ledger := controlplane.NewPostgresStore(deps.v2.db.DB)
	dispatcher, err := rilactionexecution.NewPinnedCLIDispatcher(rilactionexecution.PinnedCLIConfig{Sender: deps.grpc.server})
	if err != nil {
		panic(fmt.Sprintf("registerRILRoutes: %v", err))
	}
	executor, err := rilactionexecution.New(rilactionexecution.Config{Ledger: ledger, Dispatcher: dispatcher})
	if err != nil {
		panic(fmt.Sprintf("registerRILRoutes: %v", err))
	}
	recipes := watchdog.DefaultRecipes()
	routes.RegisterRILCommandRoutes(router, rilStore, deps.grpc.server)
	routes.RegisterGovernedActionRoutes(router, routes.GovernedActionRouteConfig{Authority: authority, Executor: executor})
	routes.RegisterRILHealRoutes(router, deps.app, recipes)
}

func registerNetworkRoutes(router *httpx.Router, deps routeDeps) {
	routes.RegisterTunnelRoutes(router, deps.tunnel)
	routes.RegisterKombifyMeRoutes(router)
	routes.RegisterAutoInstallRoutes(router, deps.app, deps.log)
}

func initFeatureService(deps routeDeps) (*features.Service, error) {
	featureStore := featureStateStore(deps)
	return features.NewService(featureStore, features.ServiceConfig{
		AllowUserOverrides: true,
	})
}

// registerWizardRoutes wires the native v2 wizard surfaces: the read-only
// projection preview (package routes) and the persisting wizard-run facade
// (package stacks). The validator stays nil (both fail closed with a precise
// reason) when the pinned-release admission is missing or rejects.
func registerWizardRoutes(router *httpx.Router, deps routeDeps, featureSvc *features.Service, managedLeases jobs.ManagedLeaseManager) {
	cfg := routes.WizardRouteConfig{
		Features: featureSvc,
		Seeds:    specv2.NewTemplateSeedSourceFromEnv(),
	}
	validator, err := specv2.NewCLIValidatorFromEnv()
	if err != nil {
		fmt.Printf("⚠️  Wizard validator admission failed (previews will 503): %v\n", err)
	} else if validator != nil {
		cfg.Validator = validator
		cfg.ReleaseVersion = validator.ReleaseVersion
	}
	routes.RegisterWizardRoutes(router, cfg)

	stores := stackControlPlaneStores(deps.v2)
	var notificationOutbox productnotifications.ProductEventEnqueuer
	if deps.monitor != nil {
		notificationOutbox = deps.monitor.notifyOutbox
	}
	stackroutes.RegisterWizardRunRoutes(router, stackroutes.WizardRunRouteConfig{
		App:                deps.app,
		Orch:               deps.orch,
		DeploymentMode:     deps.startup.cfg.DeploymentMode,
		Features:           featureSvc,
		Seeds:              cfg.Seeds,
		Validator:          cfg.Validator,
		ReleaseVersion:     cfg.ReleaseVersion,
		ManagedLeases:      managedLeases,
		NotificationOutbox: notificationOutbox,
		Trust: trust.RouteStores{
			Stacks:  stores.Stacks,
			Workers: workerControlPlaneStore(deps.v2),
			Jobs:    stores.Jobs,
		},
	})
}

func registerFeatureRoutes(router *httpx.Router, deps routeDeps, state runtimeRouteState) {
	featureSvc := deps.featureSvc
	if featureSvc == nil {
		var err error
		featureSvc, err = initFeatureService(deps)
		if err != nil {
			fmt.Printf("⚠️  Feature flags init failed: %v\n", err)
			return
		}
	}
	if featureSvc == nil {
		return
	}
	routes.RegisterFeatureFlagRoutes(router, featureSvc)
	registerWizardRoutes(router, deps, featureSvc, state.runtimeActions.LeaseManager)
	recoveryIntent := managedLeaseRecoveryIntent{
		decommissioner: state.runtimeActions.LeaseDecommissioner,
	}
	monthlyRuntimeSvc := &monthlyruntime.Service{
		Leases: state.leaseSvc,
		Runtime: &monthlyruntime.NativeRuntimeClient{
			Servers: serverRuntimeControlPlaneStore(deps.v2),
		},
		Features:        featureSvc,
		CleanupReadback: deps.providerCleanupReadback,
		Reconcile:       recoveryIntent,
	}
	routes.RegisterMonthlyRuntimeRoutes(router, monthlyRuntimeSvc)
	state.runtimeActions.RuntimeTargetResolver = monthlyRuntimeTargetResolver(
		monthlyRuntimeSvc,
		serverRuntimeControlPlaneStore(deps.v2),
		deps.log,
	)
	deps.orch.ConfigureRuntimeActions(state.runtimeActions)
	deps.log.Info("monthly_runtime_routes_registered", "runtime_authority", "techstack_provider_control")

	// Create-stack ceiling check counts against the same lease authority the
	// lifecycle routes use (late-bound: CRUD routes register earlier).
	stackroutes.ConfigureManagedRuntimeLeaseLister(state.leaseSvc)

	if deps.startup.cfg.DeploymentMode.IsSaaS() {
		stackStores := stackControlPlaneStores(deps.v2)
		demoStores := routes.StackLifecycleStores{
			Stacks:   stackStores.Stacks,
			Workers:  workerControlPlaneStore(deps.v2),
			Jobs:     stackStores.Jobs,
			Registry: serverRuntimeControlPlaneStore(deps.v2),
		}
		routes.RegisterDemoResetRoutes(router, deps.app, state.leaseSvc, recoveryIntent, demoStores)
	}
	// Shares the demo-automation secret gate, so it is wired wherever that gate
	// is configured and stays fail-closed everywhere else.
	routes.RegisterRuntimeDiagnosticsRoutes(router)
}

func featureStateStore(deps routeDeps) features.Store {
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		panic("featureStateStore: DATABASE_URL required — PocketBase fallback removed (PB retirement)")
	}
	return features.NewPostgresStore(deps.v2.db.DB)
}

func monthlyRuntimeTargetResolver(
	svc *monthlyruntime.Service,
	servers controlplane.ServerRuntimeStore,
	log *logger.Logger,
) jobs.ManagedRuntimeTargetResolver {
	if staticResolver := jobs.NewStaticManagedRuntimeTargetResolverFromEnv(); staticResolver != nil {
		log.Warn("monthly_runtime_static_target_resolver_enabled")
		return staticResolver
	}
	return jobs.NewMonthlyRuntimeTargetResolver(svc, servers)
}

func registerV2Routes(router *httpx.Router, deps routeDeps) {
	if deps.v2 == nil || deps.v2.server == nil {
		return
	}
	if deps.v2.session != nil {
		mergeLocalAuthHandlers(deps)
	}
	v2.RegisterHTTPX(router, deps.v2.server)
}

func mergeLocalAuthHandlers(deps routeDeps) {
	localSvc, err := newLocalAuthService(deps)
	if err != nil {
		deps.log.Error("v2_local_auth_invalid", "error", err)
		return
	}
	if _, err := localSvc.Bootstrap(context.Background()); err != nil {
		deps.log.Error("v2_local_auth_bootstrap_failed", "error", err)
		return
	}
	handlers := localAuthHandlers(localSvc, deps.v2.registry, deps.log)
	deps.v2.server.MergeAuthHandlers(v2.AuthHandlers{
		Methods:          handlers.MethodsHandler(),
		LocalLogin:       localOwnerLoginHandler(deps, handlers.LoginHandler()),
		LocalLogout:      handlers.LogoutHandler(),
		BreakGlassClaim:  handlers.ClaimHandler(),
		BreakGlassReveal: handlers.RevealHandler(),
	})
	deps.log.Info("v2_local_auth_enabled", "breakglass_locked", os.Getenv("TECHSTACK_BREAKGLASS_LOCKED") == "true")
}

func newLocalAuthService(deps routeDeps) (*commonauthlocal.Service, error) {
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		return nil, fmt.Errorf("local auth requires DATABASE_URL")
	}
	tenantID := strings.TrimSpace(deps.v2.defaultTenant)
	if tenantID == "" {
		tenantID = v2DefaultTenantIDFromEnv()
	}
	return commonauthlocal.New(commonauthlocal.Config{
		Store:               breakglassAuthStore(deps),
		Sessions:            deps.v2.session,
		DefaultTenantID:     tenantID,
		BootstrapEmail:      "breakglass@techstack.local",
		SessionCookieName:   deps.v2.cookieName,
		SessionCookieSecure: deps.startup.cfg.IsProduction(),
		ClaimLocked:         strings.EqualFold(strings.TrimSpace(os.Getenv("TECHSTACK_BREAKGLASS_LOCKED")), "true"),
	})
}

func localSetupProvisioner(deps routeDeps) routes.LocalSetupProvisioner {
	if deps.v2 == nil || deps.v2.session == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		return nil
	}
	return func(ctx context.Context, req routes.LocalOwnerSetup) error {
		localSvc, err := newLocalAuthService(deps)
		if err != nil {
			return err
		}
		if _, err := localSvc.Bootstrap(ctx); err != nil {
			return err
		}
		store := breakglassAuthStore(deps)
		rec, err := store.Get(ctx)
		if err != nil {
			return err
		}
		if rec != nil && rec.Claimed {
			_, err := localSvc.Authenticate(ctx, req.Email, req.Password)
			return err
		}
		if rec == nil || strings.TrimSpace(rec.ShowPassword) == "" {
			return fmt.Errorf("local setup password envelope unavailable")
		}
		return localSvc.Claim(ctx, commonauthlocal.ClaimRequest{
			CurrentPassword: rec.ShowPassword,
			NewEmail:        req.Email,
			NewPassword:     req.Password,
		})
	}
}

func breakglassAuthStore(deps routeDeps) commonauthlocal.Store {
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		panic("breakglassAuthStore: DATABASE_URL required — PocketBase fallback removed (PB retirement)")
	}
	tenantID := strings.TrimSpace(deps.v2.defaultTenant)
	if tenantID == "" {
		tenantID = v2DefaultTenantIDFromEnv()
	}
	return breakglass.NewPostgres(controlplane.NewPostgresStore(deps.v2.db.DB), tenantID)
}

// localOwnerLoginHandler delegates local owner login entirely to the
// commonauthlocal break-glass handler. The legacy PocketBase users-collection
// authentication path was removed during the PB retirement — local auth is now
// a single authority (go-common/authlocal), so no PB record lookup happens here.
func localOwnerLoginHandler(_ routeDeps, breakglass http.Handler) http.Handler {
	return breakglass
}

func localAuthHandlers(localSvc *commonauthlocal.Service, registry *providers.Registry, log *logger.Logger) *commonauthlocal.Handlers {
	if registry == nil {
		return commonauthlocal.NewHandlers(localSvc, nil, commonauthlocal.WithLoginRedirectPath("/api/v2/auth/login?provider="))
	}
	sharedRegistry, err := registry.ToOIDCClientRegistry()
	if err != nil {
		log.Error("v2_local_auth_provider_registry_invalid", "error", err)
		return commonauthlocal.NewHandlers(localSvc, nil, commonauthlocal.WithLoginRedirectPath("/api/v2/auth/login?provider="))
	}
	return commonauthlocal.NewHandlers(localSvc, sharedRegistry, commonauthlocal.WithLoginRedirectPath("/api/v2/auth/login?provider="))
}

const (
	localDeviceSessionPath   = "/api/v1/auth/device-session"
	localDeviceTokenEnv      = "TECHSTACK_LOCAL_DEVICE_TOKEN"
	localDeviceSessionHeader = "X-TechStack-Device-Token"
	localDeviceProvider      = "windows-device"
	localDeviceEmail         = "windows-device@local.invalid"
)

func registerLocalDeviceSessionRoute(router *httpx.Router, deps routeDeps) {
	registerLocalDeviceSessionRouteWithStore(router, deps, nil)
}

func registerLocalDeviceSessionRouteWithStore(router *httpx.Router, deps routeDeps, _ commonauthlocal.Store) {
	router.POST(localDeviceSessionPath, func(e *httpx.Event) error {
		if deps.v2 == nil || deps.v2.session == nil {
			return httpx.NotFound(e, "Local device session is not available")
		}
		// Positive gate: the route exists only in the local posture on a
		// self-hosted deployment with a provisioned device token. Everything
		// else — production, development, staging, SaaS, missing token — 404s.
		expectedToken := strings.TrimSpace(os.Getenv(localDeviceTokenEnv))
		if deps.startup == nil || deps.startup.cfg == nil ||
			!deps.startup.cfg.IsLocal() ||
			!deps.startup.cfg.DeploymentMode.IsSelfHosted() ||
			expectedToken == "" {
			return httpx.NotFound(e, "Local device session is not available")
		}
		if !localDeviceSessionLoopback(e.Request) {
			return httpx.Forbidden(e, "Local device session requires a loopback request")
		}
		if !localDeviceSessionAttempts.allow() {
			return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeRateLimited, "Too many failed device token attempts", nil)
		}
		if !localDeviceTokenMatches(e.Request.Header.Get(localDeviceSessionHeader), expectedToken) {
			localDeviceSessionAttempts.fail()
			return httpx.Unauthorized(e, "Invalid local device token")
		}
		localDeviceSessionAttempts.reset()

		deviceID := sha256.Sum256([]byte(expectedToken))
		claims := authsession.Claims{
			Subject:  fmt.Sprintf("device:%x", deviceID),
			TenantID: localDeviceSessionTenant(deps),
			Email:    localDeviceEmail,
			Provider: localDeviceProvider,
			Role:     "admin",
		}
		token, err := deps.v2.session.Issue(claims)
		if err != nil {
			return httpx.InternalError(e, "Local device session could not be issued")
		}
		authsession.SetSessionCookie(e.Response, localDeviceSessionCookieName(deps), token, false)
		return httpx.Success(e, http.StatusOK, map[string]any{
			"ok":       true,
			"email":    claims.Email,
			"provider": claims.Provider,
			"source":   "windows-device",
		})
	})
}

func localDeviceSessionTenant(deps routeDeps) string {
	if deps.v2 != nil && strings.TrimSpace(deps.v2.defaultTenant) != "" {
		return strings.TrimSpace(deps.v2.defaultTenant)
	}
	return "default"
}

func localDeviceSessionCookieName(deps routeDeps) string {
	if deps.v2 != nil && strings.TrimSpace(deps.v2.cookieName) != "" {
		return strings.TrimSpace(deps.v2.cookieName)
	}
	return authsession.DefaultSessionCookieName
}

// localDeviceSessionDamper applies brute-force damping to the device-token
// check: after localDeviceSessionMaxFailures consecutive failures the route
// answers 429 until the cooldown elapses. A correct token resets the damper.
type localDeviceSessionDamper struct {
	mu           sync.Mutex
	failures     int
	blockedUntil time.Time
}

const (
	localDeviceSessionMaxFailures = 5
	localDeviceSessionCooldown    = time.Minute
)

var localDeviceSessionAttempts = &localDeviceSessionDamper{}

func (d *localDeviceSessionDamper) allow() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !time.Now().Before(d.blockedUntil)
}

func (d *localDeviceSessionDamper) fail() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures++
	if d.failures >= localDeviceSessionMaxFailures {
		d.blockedUntil = time.Now().Add(localDeviceSessionCooldown)
		d.failures = 0
	}
}

func (d *localDeviceSessionDamper) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures = 0
	d.blockedUntil = time.Time{}
}

func localDeviceTokenMatches(provided, expected string) bool {
	provided = strings.TrimSpace(provided)
	expected = strings.TrimSpace(expected)
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func localDeviceSessionLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
