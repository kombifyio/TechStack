package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	commonauthflow "github.com/kombifyio/go-common/authflow"
	commonedgeauth "github.com/kombifyio/go-common/edgeauth"
	commonobservability "github.com/kombifyio/go-common/observability"
	"github.com/kombifyio/go-common/oidcclient"
	commonrole "github.com/kombifyio/go-common/role"

	"github.com/kombifyio/techstack/internal/frontend"
	"github.com/kombifyio/techstack/internal/hooks"
	"github.com/kombifyio/techstack/internal/notifications"
	pbmigration "github.com/kombifyio/techstack/internal/pocketbase_migration"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/backup"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/drift"
	"github.com/kombifyio/techstack/pkg/enrollment"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/instance"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/telemetry"
	"github.com/kombifyio/techstack/pkg/tenant"
	"github.com/kombifyio/techstack/pkg/tunnel"
	v2 "github.com/kombifyio/techstack/pkg/v2"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type startupContext struct {
	cfg                *config.Config
	identity           instance.Identity
	enrollmentResolver *enrollment.Resolver
	enrollment         enrollment.Enrollment
}

type v2Boot struct {
	db            *db.DB
	server        *v2.Server
	session       *session.Manager
	registry      *providers.Registry
	authStore     controlplane.AuthStore
	cookieName    string
	defaultTenant string
	saasMode      bool
}

type grpcBoot struct {
	server         *grpcserver.Server
	identityIssuer routes.WorkerAgentIdentityIssuer
	addr           string
}

type monitoringBoot struct {
	tsdb         *monitoring.MonitorTSDB
	queryBackend monitoring.MetricsQueryBackend
	remote       *monitoring.HTTPPromQLBackend
	status       routes.MonitoringStatusMetadata
	alertEngine  *monitoring.AlertEngine
	notifyOutbox *notifications.Outbox
	dataDir      string
}

type routeDeps struct {
	app                     *pocketbase.PocketBase
	startup                 *startupContext
	orch                    *orchestrator.Orchestrator
	grpc                    *grpcBoot
	tunnel                  *tunnel.RegistryURLResolver
	monitor                 *monitoringBoot
	v2                      *v2Boot
	workflowEngine          *workflow.Engine
	log                     *logger.Logger
	version                 string
	revision                string
	started                 time.Time
	featureSvc              *features.Service
	providerCleanupReadback monthlyruntime.CleanupReadbackSource
	providerResolution      any
	providerActions         jobs.RuntimeActions
	typedControl            *agentcontrol.Hub
}

type runtimeRouteState struct {
	leaseSvc       *vmleases.Service
	runtimeActions jobs.RuntimeActions
}

type shutdownHandles struct {
	backupScheduler *backup.Scheduler
	driftScheduler  *drift.Scheduler
	monitorCancel   context.CancelFunc
	providerControl *providerControlLifecycle
}

type providerRuntimeState struct {
	database        *db.DB
	runner          providerRuntimeRunner
	actions         jobs.RuntimeActions
	cleanupReadback monthlyruntime.CleanupReadbackSource
	resolution      any
}

// Hosted provider-control composition is an optional private extension. The
// public self-host build keeps this hook as a no-op so local control-plane
// startup remains dependency-closed without a second provider implementation.
var composeHostedProviderRuntime = func(
	context.Context,
	*sql.DB,
	config.Edition,
	*logger.Logger,
	*notifications.Outbox,
	*orchestrator.Orchestrator,
	*workflow.Engine,
) (providerRuntimeState, error) {
	return providerRuntimeState{}, nil
}

var runHostedProviderControlBootstrap = func(context.Context) error {
	return fmt.Errorf("provider-control bootstrap is unavailable in the self-hosted build")
}

func runTechstack(ctx context.Context) error {
	const defaultHTTPAddr = ":5260"
	if isStackKitOperationsProcessMode(os.Args) {
		return runStackKitOperationsProcess(ctx)
	}

	// Agent/worker mode: the same binary runs as the node-side daemon
	// (kombify Guard) instead of the control plane.
	if isAgentMode(os.Args) {
		return runAgentMode(ctx, os.Args[2:])
	}

	// Native cloud auth mode: `techstack login` / `techstack logout` run the
	// interactive RFC 8628/8252 flows (ADR-033 D1 CLI-first,
	// NATIVE-CLIENT-PLATFORM-STANDARD section 4) and exit.
	if isLoginMode(os.Args) {
		return runLoginMode(ctx, os.Args[1:])
	}

	os.Args = applyDefaultServeHTTP(os.Args, defaultHTTPAddr)
	if handled, err := handleEarlyCommand(ctx, os.Args); handled {
		return err
	}

	app := newTechstackApp()
	otelShutdown, otelErr := telemetry.Init(ctx, "kombify-techstack", version)
	if otelErr != nil {
		return fmt.Errorf("initialize telemetry: %w", otelErr)
	}
	defer func() { _ = otelShutdown(context.Background()) }()
	sentryShutdown := commonobservability.MustInit(commonobservability.Config{
		Service:     "kombify-techstack",
		DSN:         strings.TrimSpace(os.Getenv("SENTRY_DSN")),
		Environment: techstackSentryEnvironment(),
		Release:     techstackSentryRelease(),
	})
	defer sentryShutdown()

	log := logger.Default()
	startup, err := loadStartupContext(log)
	if err != nil {
		return err
	}
	localPostgres, err := maybeStartEmbeddedPostgres(startup.cfg, log)
	if err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() {
		if localPostgres != nil {
			if err := localPostgres.Stop(); err != nil {
				log.Warn("embedded_postgres_stop_failed", "error", err)
			}
		}
	}()

	// Control-plane Postgres is the source of truth (PB retirement). Connect and
	// run pkg/db migrations fail-closed: without DATABASE_URL the runtime cannot
	// serve the control-plane API.
	v2Boot := bootV2(ctx, app, startup.cfg, log)
	if v2Boot.db == nil || v2Boot.db.DB == nil {
		return fmt.Errorf("control-plane database is required: set %s with a Postgres DSN (PocketBase control-plane fallback removed)", db.EnvDatabaseURL)
	}
	tenantErr := ensureDefaultControlPlaneTenant(ctx, startup.cfg, v2Boot)
	if tenantErr != nil {
		return fmt.Errorf("bootstrap control-plane tenant: %w", tenantErr)
	}

	// Bootstrap the PocketBase data layer (no HTTP serving). During the
	// coexistence window the orchestrator, RIL stores, auth records, and
	// collection hooks still read/write the embedded PB store; httpx owns the
	// HTTP shell. Bootstrap initializes the embedded data layer. PocketBase
	// collection migrations (internal/migrations) have been retired — the
	// control-plane schema is owned by pkg/db/migrations/*.sql, applied above
	// during bootV2 -> openV2DB -> db.Migrate.
	if err := app.Bootstrap(); err != nil {
		return fmt.Errorf("bootstrap pocketbase data layer: %w", err)
	}

	controlStores := stackControlPlaneStores(v2Boot)
	orchCfg := orchestrator.DefaultConfig()
	orchCfg.StackStore = controlStores.Stacks
	orchCfg.JobStore = controlStores.Jobs
	orchCfg.WorkerStore = workerControlPlaneStore(v2Boot)
	orchCfg.WalletStore = walletControlPlaneStore(v2Boot)
	orchCfg.RegistryStore = registryControlPlaneStore(v2Boot)
	orchCfg.RoutingStore = controlStores.Routing
	orch := orchestrator.New(app, orchCfg, log)
	typedControl := agentcontrol.NewHub(v2Boot.db.DB)
	tunnelResolver := bootTunnelResolver(log)
	grpcState, err := bootAgentGRPC(startup.cfg, v2Boot, log)
	if err != nil {
		log.Error("agent_mtls_config_invalid", "error", err)
		return err
	}
	notificationOutbox := bootProductNotificationOutbox(v2Boot, log)
	rilSignalOutbox, rilSignalWorker := bootRILSignalRuntime(v2Boot, buildRevision, log)
	monitorState := bootMonitoring(startup.cfg, grpcState.server, log, notificationOutbox, rilSignalOutbox)
	workflowEngine := bootWorkflowEngine(v2Boot, log)
	providerState, err := composeHostedProviderRuntime(
		ctx,
		v2Boot.db.DB,
		startup.cfg.Edition,
		log,
		notificationOutbox,
		orch,
		workflowEngine,
	)
	if err != nil {
		return err
	}
	providerDatabase := providerState.database
	// Until ownership transfers to the shutdown lifecycle below, every later
	// startup error must close the dedicated pool.
	defer func() {
		if providerDatabase != nil {
			_ = providerDatabase.Close()
		}
	}()

	deps := routeDeps{
		app:                     app,
		startup:                 startup,
		orch:                    orch,
		grpc:                    grpcState,
		tunnel:                  tunnelResolver,
		monitor:                 monitorState,
		v2:                      v2Boot,
		workflowEngine:          workflowEngine,
		log:                     log,
		version:                 version,
		revision:                buildRevision,
		started:                 startTime,
		providerCleanupReadback: providerState.cleanupReadback,
		providerResolution:      providerState.resolution,
		providerActions:         providerState.actions,
		typedControl:            typedControl,
	}

	router, err := buildRouter(deps)
	if err != nil {
		return err
	}
	var lanBridge *localLANBridge
	if startup.cfg.IsLocal() && startup.cfg.DeploymentMode.IsSelfHosted() {
		lanBridge = newLocalLANBridge(router, startup.cfg.Server.DataDir, tunnelResolver, log)
		routes.RegisterLocalLANBridgeRoutes(router, lanBridge)
		if restoreErr := lanBridge.Restore(ctx); restoreErr != nil {
			log.Warn("local_lan_bridge_restore_failed", "error", restoreErr)
		}
	}

	// Register PB collection bootstrap + business hooks against the bootstrapped
	// data layer (coexistence). These previously ran inside OnServe.
	registerPocketBaseBootstrap(app)
	if err := registerBusinessHooks(app, startup.cfg, log); err != nil {
		return err
	}
	if err := runTenantBackfillCheck(app, startup.cfg, log); err != nil {
		return err
	}

	registrySweeper := bootServerRegistrySweeper(v2Boot, monitorState, log)
	jobReclaimer := bootJobExecutionReclaimer(v2Boot, orch, monitorState, log)
	if observer := jobExecutionDeferObserver(monitorState); observer != nil {
		orch.Queue().SetExecutionDeferObserver(observer)
	}
	handles := startRuntimeLifecycle(ctx, app, startup.cfg, orch, grpcState, monitorState, workflowEngine, rilSignalWorker, providerState.runner, registrySweeper, jobReclaimer, log)

	addr := startup.cfg.Server.ListenAddr
	if addr == "" {
		addr = defaultHTTPAddr
	}
	srv := httpx.NewServer(router, httpx.ServerConfig{Addr: addr})
	printStartup(addr, deps)

	return serveWithGracefulShutdown(ctx, srv, func() {
		if lanBridge != nil {
			if bridgeErr := lanBridge.Shutdown(context.Background()); bridgeErr != nil {
				log.Warn("local_lan_bridge_shutdown_failed", "error", bridgeErr)
			}
		}
		stopRuntimeLifecycle(orch, grpcState, tunnelResolver, monitorState, providerDatabase, v2Boot, handles)
		providerDatabase = nil
	}, log)
}

// serveWithGracefulShutdown starts the httpx server and blocks until the
// context is cancelled or a termination signal is received, then drains the
// server and runs the provided cleanup.
func serveWithGracefulShutdown(ctx context.Context, srv *httpx.Server, cleanup func(), log *logger.Logger) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Start() }()

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		cleanup()
		return err
	case <-signalCtx.Done():
		log.Info("shutdown_signal_received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("http_shutdown_error", "error", err)
		}
		cleanup()
		return nil
	}
}

func applyDefaultServeHTTP(args []string, defaultHTTPAddr string) []string {
	if len(args) < 2 || args[1] != "serve" {
		return args
	}
	for _, arg := range args {
		if arg == "--http" || strings.HasPrefix(arg, "--http=") {
			return args
		}
	}
	out := append([]string{}, args[:2]...)
	out = append(out, "--http="+defaultHTTPAddr)
	out = append(out, args[2:]...)
	return out
}

func handleEarlyCommand(ctx context.Context, args []string) (bool, error) {
	if len(args) <= 1 {
		return false, nil
	}
	switch args[1] {
	case "version":
		fmt.Printf("kombifyTechstack v%s\n", version)
		fmt.Println("The Hybrid Infrastructure Unifier")
		fmt.Printf("Go: %s\n", runtime.Version())
		return true, nil
	case "provider-control-bootstrap":
		if err := runHostedProviderControlBootstrap(ctx); err != nil {
			return true, err
		}
		fmt.Println("provider-control runtime authority installed and verified")
		return true, nil
	default:
		return false, nil
	}
}

func newTechstackApp() *pocketbase.PocketBase {
	return pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       techstackPBDataDirFromEnv(),
		DefaultEncryptionEnv: "TECHSTACK_ENCRYPTION_KEY",
		DefaultDev:           os.Getenv("TECHSTACK_ENV") == "development",
		DefaultQueryTimeout:  30,
		HideStartBanner:      true,
	})
}

func techstackPBDataDirFromEnv() string {
	if dataDir := strings.TrimSpace(os.Getenv("TECHSTACK_PB_DATA_DIR")); dataDir != "" {
		return dataDir
	}
	if dataDir := strings.TrimSpace(os.Getenv("TECHSTACK_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "pb_data")
	}
	return "pb_data"
}

func techstackSentryEnvironment() string {
	if env := strings.TrimSpace(os.Getenv("SENTRY_ENVIRONMENT")); env != "" {
		return env
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_ENV"))) {
	case "development", "dev", "local":
		return "dev"
	case "preview":
		return "preview"
	default:
		return "prod"
	}
}

func techstackSentryRelease() string {
	if release := v2FirstEnv("SENTRY_RELEASE", "TECHSTACK_RELEASE", "TECHSTACK_BUILD_REVISION", "RENDER_GIT_COMMIT", "GIT_COMMIT"); release != "" {
		return release
	}
	if revision := strings.TrimSpace(buildRevision); revision != "" && revision != "dev" {
		return revision
	}
	return ""
}

func loadStartupContext(log *logger.Logger) (*startupContext, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateLocalPosture(); err != nil {
		return nil, err
	}
	identity, err := instance.NewResolver(cfg.Server.DataDir).Load()
	if err != nil {
		return nil, fmt.Errorf("load instance identity: %w", err)
	}
	log.Info("instance_identity_loaded", "instance_id", identity.ID, "created_at", identity.CreatedAt.Format(time.RFC3339))

	resolver := enrollment.NewResolver(cfg.Server.DataDir, identity.ID)
	record, err := resolver.Load()
	if err != nil {
		return nil, fmt.Errorf("load enrollment record: %w", err)
	}
	log.Info("enrollment_loaded", "instance_id", record.InstanceID, "mode", string(record.Mode))

	return &startupContext{
		cfg:                cfg,
		identity:           identity,
		enrollmentResolver: resolver,
		enrollment:         record,
	}, nil
}

func bootV2(ctx context.Context, app core.App, cfg *config.Config, log *logger.Logger) *v2Boot {
	boot := &v2Boot{}
	if !v2EnabledFromEnv() {
		return boot
	}
	boot.defaultTenant = v2DefaultTenantIDFromEnv()
	boot.saasMode = cfg.DeploymentMode.IsSaaS()

	options := []v2.Option{}
	boot.db = openV2DB(ctx, cfg, log)
	if boot.db != nil {
		options = append(options, v2.WithDB(boot.db))
	}

	boot.cookieName = v2SessionCookieNameFromEnv()
	options = append(options, v2.WithSessionCookieName(boot.cookieName))
	configureV2Auth(ctx, cfg, log, boot, &options)
	boot.server = v2.NewServer(options...)
	return boot
}

func openV2DB(ctx context.Context, cfg *config.Config, log *logger.Logger) *db.DB {
	dbCfg, err := db.ConfigFromEnv(cfg.Server.DataDir)
	if err != nil {
		log.Error("v2_db_config_invalid", "error", err)
		return nil
	}
	if dbCfg.Backend != db.StoreBackendPostgres {
		return nil
	}
	opened, err := db.Open(dbCfg)
	if err != nil {
		log.Error("v2_db_open_failed", "error", err)
		return nil
	}
	if err := opened.Migrate(ctx); err != nil {
		log.Error("v2_db_migrate_failed", "error", err)
		_ = opened.Close()
		return nil
	}
	log.Info("v2_db_ready", "backend", string(opened.Backend()))
	return opened
}

func ensureDefaultControlPlaneTenant(ctx context.Context, cfg *config.Config, boot *v2Boot) error {
	const (
		selfHostedTenantKind = "self_hosted"
		saasTenantKind       = "saas"
		activeStatus         = "active"
	)
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return fmt.Errorf("control-plane database is required")
	}
	tenantID := strings.TrimSpace(boot.defaultTenant)
	if tenantID == "" {
		tenantID = v2DefaultTenantIDFromEnv()
		boot.defaultTenant = tenantID
	}
	tenantKind := selfHostedTenantKind
	if cfg != nil && cfg.DeploymentMode.IsSaaS() {
		tenantKind = saasTenantKind
	}
	_, err := controlplane.NewPostgresStore(boot.db.DB).EnsureTenant(ctx, controlplane.Tenant{
		ID: tenantID, DisplayName: tenantID, Kind: tenantKind, Status: activeStatus,
	})
	return err
}

func configureV2Auth(ctx context.Context, cfg *config.Config, log *logger.Logger, boot *v2Boot, options *[]v2.Option) {
	secret := strings.TrimSpace(os.Getenv("TECHSTACK_V2_SESSION_SECRET"))
	if secret == "" {
		return
	}
	sessionCfg := sessionpolicy.BrowserSessionConfig(
		v2SessionAudienceFromEnv(),
		[]byte(secret),
	)
	mgr, err := session.NewManager(session.Config(sessionCfg))
	if err != nil {
		log.Error("v2_session_manager_invalid", "error", err)
		return
	}

	boot.session = mgr
	*options = append(*options, v2.WithSession(mgr))

	registry, source, err := v2AuthRegistry(ctx, boot.db)
	if err != nil {
		log.Error("v2_auth_provider_invalid", "error", err)
		return
	}
	registry, enabled := gatedV2Registry(cfg, log, registry)
	if !enabled || registry == nil || registry.Len() == 0 {
		return
	}

	boot.registry = registry
	defaultProviderID := v2DefaultProviderID(registry)
	sharedRegistry, err := registry.ToOIDCClientRegistry()
	if err != nil {
		log.Error("v2_auth_provider_registry_invalid", "error", err)
		return
	}
	var authStore controlplane.AuthStore
	if boot.db != nil && boot.db.DB != nil {
		authStore = controlplane.NewPostgresStore(boot.db.DB)
	}
	boot.authStore = authStore
	// SaaS logins resolve the Auth0 organization at login time so standalone
	// sessions land on the same tenant as embedded gateway requests
	// (one-truth rule). Self-hosted keeps the requested/default tenant flow.
	var tenantResolver commonauthflow.TenantResolver
	if boot.saasMode {
		var orgLister organizationLister
		if mgmt := newAuth0MgmtClientFromEnv(); mgmt != nil {
			orgLister = mgmt
		}
		tenantResolver = v2CloudTenantResolver(authStore, orgLister, boot.defaultTenant)
	}
	authFlow, err := commonauthflow.NewService(commonauthflow.Config{
		Providers:           sharedRegistry,
		Sessions:            mgr,
		StateSecret:         []byte(secret),
		DefaultProviderID:   defaultProviderID,
		DefaultTenantID:     boot.defaultTenant,
		DefaultReturnTo:     "/stacks",
		LoginErrorPath:      "/login",
		CallbackPath:        config.CanonicalAuthCallbackPath,
		SessionCookieName:   boot.cookieName,
		SessionCookieSecure: cfg.IsProduction(),
		TenantResolver:      tenantResolver,
		UserUpsert:          v2CloudUserUpsert(authStore, boot.defaultTenant),
		Exchanger:           newRotatingOIDCCodeExchanger(nil, v2FirstEnv(techstackAuthCloudClientSecretNext)),
	})
	if err != nil {
		log.Error("v2_auth_flow_invalid", "error", err)
		return
	}
	*options = append(*options, v2.WithAuthHandlers(v2.AuthHandlers{
		Providers: authFlow.ProvidersHandler(),
		Login:     v2AuthPublicOriginHandler(authFlow.LoginHandler(), config.PublicOriginFromEnv()),
		Callback:  v2AuthPublicOriginHandler(authFlow.CallbackHandler(), config.PublicOriginFromEnv()),
		Logout:    authFlow.LogoutHandler(),
	}))
	log.Info("v2_auth_enabled", "providers", registry.Len(), "source", source, "default_provider", defaultProviderID, "default_tenant", boot.defaultTenant)
}

func v2AuthPublicOriginHandler(next http.Handler, publicOrigin string) http.Handler {
	if next == nil {
		return nil
	}
	origin := config.SanitizeOrigin(v2AuthPublicOrigin(publicOrigin))
	if origin == "" {
		return next
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRenderPreviewAuthHost(r) {
			next.ServeHTTP(w, r)
			return
		}
		req := r.Clone(r.Context())
		req.Host = parsed.Host
		req.URL.Scheme = parsed.Scheme
		req.URL.Host = parsed.Host
		req.Header = r.Header.Clone()
		req.Header.Set("X-Forwarded-Proto", parsed.Scheme)
		req.Header.Set("X-Forwarded-Host", parsed.Host)
		next.ServeHTTP(w, req)
	})
}

func v2AuthPublicOrigin(publicOrigin string) string {
	if origin := renderPreviewPublicOriginFromEnv(); origin != "" {
		return origin
	}
	return publicOrigin
}

func renderPreviewPublicOriginFromEnv() string {
	edition := strings.ToLower(strings.TrimSpace(v2FirstEnv("KOMBIFY_EDITION", "PUBLIC_KOMBIFY_EDITION")))
	if edition != string(config.EditionPreview) {
		return ""
	}
	for _, key := range []string{
		"RENDER_EXTERNAL_URL",
		"RENDER_PREVIEW_ORIGIN",
		"RENDER_PREVIEW_URL",
		"TECHSTACK_RENDER_PREVIEW_ORIGIN",
		"TECHSTACK_RENDER_PREVIEW_URL",
	} {
		origin := config.SanitizeOrigin(os.Getenv(key))
		if isRenderPreviewOrigin(origin) {
			return origin
		}
	}
	return ""
}

func isRenderPreviewAuthHost(r *http.Request) bool {
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return isRenderPreviewHost(host)
}

func isRenderPreviewOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return isRenderPreviewHost(parsed.Host)
}

func isRenderPreviewHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i > -1 {
		host = host[:i]
	}
	return strings.Contains(host, "-pr-") && strings.HasSuffix(host, ".onrender.com")
}

func v2DefaultTenantIDFromEnv() string {
	defaultTenantID := v2FirstEnv("TECHSTACK_V2_DEFAULT_TENANT_ID", "TECHSTACK_AUTH_CLOUD_PROJECT_ID")
	if defaultTenantID == "" {
		return "default"
	}
	return defaultTenantID
}

func gatedV2Registry(cfg *config.Config, log *logger.Logger, registry *providers.Registry) (*providers.Registry, bool) {
	registry, gateResult := applySelfHostedCloudLoginGate(cfg.DeploymentMode, registry)
	if !gateResult.Enabled {
		log.Info("v2_auth_cloud_login_disabled", "reason", gateResult.Reason)
		return nil, false
	}
	return registry, true
}

func bootTunnelResolver(log *logger.Logger) *tunnel.RegistryURLResolver {
	tunnelCfg := tunnel.DefaultConfig()
	if customURL := os.Getenv("TECHSTACK_REGISTRY_URL"); customURL != "" {
		tunnelCfg.Mode = tunnel.NetworkModeCustom
		tunnelCfg.CustomURL = customURL
	} else if mode := os.Getenv("TECHSTACK_NETWORK_MODE"); mode != "" {
		tunnelCfg.Mode = tunnel.NetworkMode(mode)
	}
	return tunnel.NewRegistryURLResolver(tunnelCfg, log)
}

func bootAgentGRPC(cfg *config.Config, boot *v2Boot, log *logger.Logger) (*grpcBoot, error) {
	state := &grpcBoot{addr: v2FirstEnv("TECHSTACK_GRPC_ADDR")}
	if state.addr == "" {
		state.addr = ":5263"
	}
	certFile := os.Getenv("TECHSTACK_TLS_CERT")
	keyFile := os.Getenv("TECHSTACK_TLS_KEY")
	caFile := os.Getenv("TECHSTACK_TLS_CA")

	// When no operator-supplied TLS files are set, try the self-managed agent
	// CA: production activates only with a deployment-supplied persistent CA; dev
	// opts in via TECHSTACK_AGENT_CA_SELF_MANAGED. This turns kombify Guard
	// mTLS on without hand-provisioned certificates.
	var selfManagedCM *auth.CertManager
	if certFile == "" && keyFile == "" && caFile == "" {
		cm, enabled, caErr := agentCAFromEnv(cfg, techstackPBDataDirFromEnv())
		if caErr != nil {
			return state, fmt.Errorf("agent self-managed CA: %w", caErr)
		}
		if enabled {
			commonName, sans := grpcServerCommonNameAndSANs()
			tlsDir := filepath.Join(techstackPBDataDirFromEnv(), "agent-grpc-tls")
			cf, kf, af, wErr := writeSelfManagedServerTLS(cm, commonName, sans, tlsDir)
			if wErr != nil {
				return state, fmt.Errorf("agent self-managed TLS: %w", wErr)
			}
			certFile, keyFile, caFile = cf, kf, af
			selfManagedCM = cm
			log.Info("agent_grpc_self_managed_ca",
				"message", "agent gRPC using self-managed CA",
				"server_cn", commonName,
				"sans", strings.Join(sans, ","),
			)
		}
	}

	if err := agentMTLSConfigOK(cfg, certFile, keyFile, caFile); err != nil {
		return state, err
	}
	if !shouldStartAgentGRPC(cfg, certFile, keyFile, caFile) {
		log.Warn("agent_grpc_disabled_mtls_missing",
			"message", "agent gRPC disabled in production because no mTLS certificates are configured (set TECHSTACK_TLS_* files or the self-managed CA via TECHSTACK_AGENT_CA_CERT/KEY)",
			"required_env", "TECHSTACK_TLS_CERT, TECHSTACK_TLS_KEY, TECHSTACK_TLS_CA or TECHSTACK_AGENT_CA_CERT, TECHSTACK_AGENT_CA_KEY",
		)
		return state, nil
	}

	requireMTLS := shouldRequireAgentMTLS(cfg, certFile, keyFile, caFile)
	grpcSrv, err := grpcserver.New(grpcserver.Config{
		ListenAddr:            state.addr,
		CertFile:              certFile,
		KeyFile:               keyFile,
		CAFile:                caFile,
		RequireMTLS:           requireMTLS,
		QueueMaxSize:          cfg.Server.GRPCQueueMaxSize,
		QueueOverflowStrategy: cfg.Server.GRPCQueueOverflowStrategy,
		QueueWarningThreshold: cfg.Server.GRPCQueueWarningThreshold,
		TofuQueueMaxSize:      cfg.Server.GRPCTofuQueueMaxSize,
		RuntimeLogPath:        cfg.Server.RuntimeLogPath,
		RuntimeLogMaxEntries:  cfg.Server.RuntimeLogMaxEntries,
	}, log)
	if err != nil {
		if requireMTLS {
			return state, fmt.Errorf("create gRPC server with required mTLS: %w", err)
		}
		fmt.Printf("⚠️  gRPC server creation failed: %v (agents will not be able to connect)\n", err)
		return state, nil
	}
	state.server = grpcSrv
	if selfManagedCM != nil {
		// Wire the CA so Register enforces revocation and cert validation
		// against the self-managed CA.
		grpcSrv.SetCertManager(selfManagedCM)
	}
	enrollmentStoreWired := wireAgentEnrollmentStore(grpcSrv, boot, log)
	state.identityIssuer = configuredAgentIdentityIssuer(grpcSrv, selfManagedCM, enrollmentStoreWired)
	return state, nil
}

func wireAgentEnrollmentStore(grpcSrv *grpcserver.Server, boot *v2Boot, log *logger.Logger) bool {
	if grpcSrv == nil || boot == nil || boot.db == nil {
		return false
	}
	grpcSrv.SetEnrollmentStore(grpcserver.NewPostgresAgentEnrollmentStore(boot.db))
	if boot.defaultTenant != "" {
		grpcSrv.SetDefaultTenant(boot.defaultTenant)
	}
	log.Info("agent_enrollment_store_wired", "backend", "postgres", "default_tenant", boot.defaultTenant)
	return true
}

// configuredAgentIdentityIssuer exposes the optional gRPC certificate issuer
// only when the same server has both a signing CA and its durable enrollment
// store. SaaS commonly runs the HTTPS Guard while a separately configured gRPC
// listener has transport certificates but no agent-signing CA. Passing that
// partially configured server to worker registration makes the optional mTLS
// bundle fail the otherwise valid HTTPS enrollment.
func configuredAgentIdentityIssuer(
	grpcSrv *grpcserver.Server,
	certManager *auth.CertManager,
	enrollmentStoreWired bool,
) routes.WorkerAgentIdentityIssuer {
	if grpcSrv == nil || certManager == nil || !enrollmentStoreWired {
		return nil
	}
	return grpcSrv
}

// buildRouter constructs the httpx.Router, installs the global middleware
// chain, and registers every route group. It is the PB-free replacement for the
// OnServe-bound bindRoutes/bindGlobalMiddleware path.
func buildRouter(deps routeDeps) (*httpx.Router, error) {
	inventoryPolicy, err := inventoryPolicyFromEnvironment(deps.startup.cfg.DeploymentMode)
	if err != nil {
		return nil, fmt.Errorf("initialize inventory authorization: %w", err)
	}
	router := httpx.NewRouter()
	bindGlobalMiddleware(router, deps)
	featureSvc, featureErr := initFeatureService(deps)
	if featureErr != nil {
		deps.log.Error("feature_routes_init_failed", "error", featureErr)
	} else {
		deps.featureSvc = featureSvc
	}
	state, err := registerVMLeaseRoutes(router, deps)
	if err != nil {
		return nil, err
	}
	registerCoreRoutes(router, deps, state.runtimeActions.LeaseManager)
	registerRuntimeInventoryRoutes(router, deps, state, inventoryPolicy)
	registerRILRoutes(router, deps)
	registerNetworkRoutes(router, deps)
	registerFeatureRoutes(router, deps, state)
	registerV2Routes(router, deps)
	registerFrontendFallback(router, deps)
	return router, nil
}

func registerFrontendFallback(router *httpx.Router, deps routeDeps) {
	handler, ok := frontend.Handler()
	if !ok {
		return
	}
	deps.log.Info("embedded_frontend_registered")
	router.SetNotFound(func(e *httpx.Event) error {
		if !frontendFallbackEligible(e.Request) {
			return httpx.NotFound(e, "Not found")
		}
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})
}

func frontendFallbackEligible(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p == "" {
		return true
	}
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/_internal/") {
		return false
	}
	switch p {
	case "/api", "/metrics", "/health", "/healthz", "/livez", "/readyz", "/openapi.json", "/install.sh", "/install.ps1":
		return false
	default:
		return true
	}
}

func bindGlobalMiddleware(router *httpx.Router, deps routeDeps) {
	cfg := deps.startup.cfg
	tenantguard.Configure(cfg.DeploymentMode.IsSaaS())
	if deps.v2 != nil {
		sessionreauth.Configure(deps.v2.cookieName, cfg.IsProduction())
	}
	router.BindFunc(func(e *httpx.Event) (err error) {
		defer func() {
			if r := recover(); r != nil {
				_ = httpx.InternalError(e, "internal server error")
				err = nil
			}
		}()
		return e.Next()
	})
	router.BindFunc(middleware.SentryMiddleware)
	router.BindFunc(middleware.OTelMiddleware)
	router.BindFunc(func(e *httpx.Event) error {
		middleware.ApplyRequestSecurityHeaders(e.Response.Header(), e.Request, cfg.DeploymentMode)
		return e.Next()
	})
	router.BindFunc(middleware.RequestIDMiddleware)
	bindCORS(router, cfg)
	router.BindFunc(middleware.EdgeIdentityMiddlewareFromConfig(cfg))
	router.BindFunc(v2SessionIdentityMiddleware(deps.v2))
	bindRateLimiter(router, cfg)
	bindCSRF(router, cfg)
}

func v2SessionIdentityMiddleware(boot *v2Boot) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		if boot == nil || boot.session == nil || e == nil || e.Request == nil {
			return e.Next()
		}
		if id := identity.FromContext(e.Request.Context()); id != nil && id.IsAuthenticated() {
			membership := enrichIdentityFromMembership(e.Request.Context(), boot.authStore, id, id.OrgID, boot.defaultTenant, "default")
			// Edge-signed tenants are authoritative per request: never rebind
			// them to another tenant; an unrecoverable projection failure is
			// answered with the retryable re-auth signal (a fresh login
			// re-mints the projection through v2CloudUserUpsert).
			membership, projectionErr := resolveIdentityTenantProjection(e, boot, id, membership, false)
			if projectionErr != nil {
				logger.Default().Error("tenant_identity_projection_failed", "error", projectionErr, "tenant_id", id.OrgID)
				return sessionreauth.Denial(e, id.OrgID, projectionErr)
			}
			ctx := identity.NewContext(e.Request.Context(), id)
			ctx = contextWithMembershipAuthorization(ctx, membership)
			e.Request = e.Request.WithContext(ctx)
			return e.Next()
		}
		token, ok := v2SessionTokenFromRequest(e.Request, boot.cookieName)
		if !ok {
			return e.Next()
		}
		claims, err := boot.session.Verify(token)
		if err != nil {
			return e.Next()
		}
		id := identityFromV2SessionClaims(claims)
		membership := enrichV2SessionIdentityFromMembership(e.Request.Context(), boot.authStore, boot.defaultTenant, boot.saasMode, claims, id)
		// Session claims are login-time snapshots, not per-request authority:
		// when their tenant no longer projects, the recovery ladder may rebind
		// the identity the same way a fresh login would.
		membership, err = resolveIdentityTenantProjection(e, boot, id, membership, true)
		if err != nil {
			logger.Default().Error("tenant_identity_projection_failed", "error", err, "tenant_id", id.OrgID)
			return sessionreauth.Denial(e, id.OrgID, err)
		}
		hydrateIdentityTenantFromMembership(id, membership)
		ctx := identity.NewContext(e.Request.Context(), id)
		ctx = contextWithMembershipAuthorization(ctx, membership)
		e.Request = e.Request.WithContext(ctx)
		return e.Next()
	}
}

// resolveIdentityTenantProjection is the shared middleware seam that turns a
// signed identity into a projected control-plane tenant. It hydrates the
// tenant, applies the owner fallback, projects it, and - when the initial
// projection fails - attempts the server-side recovery ladder before the
// request is answered with the session_reprojection_required signal.
func resolveIdentityTenantProjection(
	e *httpx.Event,
	boot *v2Boot,
	id *identity.Identity,
	membership *controlplane.Membership,
	allowTenantRebind bool,
) (*controlplane.Membership, error) {
	hydrateIdentityTenantFromMembership(id, membership)
	applyOwnerTenantFallback(id, boot.saasMode)
	projected, err := ensureIdentityTenantProjection(e.Request.Context(), boot.authStore, id, membership)
	if err == nil {
		hydrateIdentityTenantFromMembership(id, projected)
		return projected, nil
	}
	if !allowTenantRebind {
		return nil, err
	}
	failedTenant := strings.TrimSpace(id.OrgID)
	recovered, rung, recoveryErr := recoverIdentityTenantProjection(e.Request.Context(), boot, id)
	if recoveryErr != nil {
		return nil, err
	}
	hydrateIdentityTenantFromMembership(id, recovered)
	sessionreauth.RecordRecovered(e, failedTenant, id.OrgID, rung)
	return recovered, nil
}

// recoverIdentityTenantProjection is the server-side re-projection ladder for
// a signature-valid session whose tenant projection failed (e.g. its tenant
// rows were removed by a migration or the runtime-db incident 2026-08-12).
// It never invents access: it only rebinds the identity to tenants derived
// from the user's own remaining memberships or the canonical per-user owner
// tenant (the same rungs a fresh login resolves).
func recoverIdentityTenantProjection(ctx context.Context, boot *v2Boot, id *identity.Identity) (*controlplane.Membership, string, error) {
	if boot == nil || id == nil || !id.IsAuthenticated() {
		return nil, "", fmt.Errorf("identity is not recoverable")
	}
	failedTenant := strings.TrimSpace(id.OrgID)
	// Rung 1: the user still holds an active org membership in the control
	// plane; re-derive the tenant from it exactly like a fresh login would.
	if membership := preferredOrgMembershipForIdentity(ctx, boot.authStore, id); membership != nil {
		id.OrgID = strings.TrimSpace(membership.TenantID)
		if projected, err := ensureIdentityTenantProjection(ctx, boot.authStore, id, membership); err == nil {
			return projected, "membership_rederivation", nil
		}
		id.OrgID = failedTenant
	}
	// Rung 2 (SaaS): owner-tenant fallback - bootstrap the user's personal
	// tenant, mirroring applyOwnerTenantFallback for tenant-less identities.
	if boot.saasMode {
		if userID := strings.TrimSpace(id.UserID); userID != "" {
			id.OrgID = canonicalOwnerTenantID(userID)
			if projected, err := ensureIdentityTenantProjection(ctx, boot.authStore, id, nil); err == nil {
				return projected, "owner_tenant_bootstrap", nil
			}
			id.OrgID = failedTenant
		}
	}
	return nil, "", fmt.Errorf("session tenant %q is not recoverable server-side", failedTenant)
}

// ensureIdentityTenantProjection makes the signed Edge organization a real
// Postgres tenant before any tenant-scoped runtime write occurs. Cloud login
// historically created only the shared fallback membership; copying that
// membership into the signed organization preserves its role/entitlements
// while keeping stacks, servers, leases, and services owner-isolated.
func ensureIdentityTenantProjection(ctx context.Context, store controlplane.AuthStore, id *identity.Identity, membership *controlplane.Membership) (*controlplane.Membership, error) {
	const activeStatus = "active"

	if store == nil || id == nil || !id.IsAuthenticated() {
		return membership, nil
	}
	tenantID := strings.TrimSpace(id.OrgID)
	if tenantID == "" {
		return membership, nil
	}
	if membership == nil {
		subjectID := strings.TrimSpace(id.UserID)
		if demoguard.IsDemoUser(subjectID) && demoguard.IsDemoTenant(tenantID) {
			return bootstrapIdentityTenantProjection(ctx, store, id, tenantID, subjectID)
		}
		if isOwnerTenantID(tenantID, subjectID) {
			// Owner tenant (applyOwnerTenantFallback): materialize the user's
			// personal tenant on first contact, mirroring the demo bootstrap.
			return bootstrapIdentityTenantProjection(ctx, store, id, tenantID, subjectID)
		}
		return nil, fmt.Errorf("signed edge tenant %q has no eligible control-plane membership", tenantID)
	}
	if strings.TrimSpace(membership.TenantID) == tenantID {
		return membership, nil
	}
	if _, err := store.UpsertTenant(ctx, controlplane.Tenant{
		ID: tenantID, ExternalOrgID: tenantID, DisplayName: tenantID, Kind: "saas", Status: activeStatus,
	}); err != nil {
		return nil, fmt.Errorf("upsert signed edge tenant: %w", err)
	}
	projected := *membership
	projected.ID = tenantID + ":" + strings.TrimSpace(id.UserID)
	projected.TenantID = tenantID
	projected.UserID = strings.TrimSpace(id.UserID)
	projected.Status = activeStatus
	created, err := store.UpsertMembership(ctx, projected)
	if err != nil {
		return nil, fmt.Errorf("project cloud membership into signed edge tenant: %w", err)
	}
	return created, nil
}

// bootstrapIdentityTenantProjection materializes tenant + user + membership
// for identities whose tenant has no control-plane rows yet: the demo tenant
// and the per-user owner tenant (applyOwnerTenantFallback).
func bootstrapIdentityTenantProjection(
	ctx context.Context,
	store controlplane.AuthStore,
	id *identity.Identity,
	tenantID string,
	subjectID string,
) (*controlplane.Membership, error) {
	const activeStatus = "active"

	if _, err := store.UpsertTenant(ctx, controlplane.Tenant{
		ID: tenantID, ExternalOrgID: tenantID, DisplayName: tenantID, Kind: "saas", Status: activeStatus,
	}); err != nil {
		return nil, fmt.Errorf("bootstrap tenant: %w", err)
	}
	if _, err := store.UpsertUser(ctx, controlplane.User{
		ID: subjectID, PrimaryEmail: strings.TrimSpace(id.Email), Status: activeStatus,
	}); err != nil {
		return nil, fmt.Errorf("bootstrap user: %w", err)
	}
	membership, err := store.UpsertMembership(ctx, controlplane.Membership{
		ID:          tenantID + ":" + subjectID,
		TenantID:    tenantID,
		UserID:      subjectID,
		RoleKey:     "member",
		ProviderKey: "cloud",
		SubjectID:   subjectID,
		Status:      activeStatus,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap membership: %w", err)
	}
	return membership, nil
}

func v2SessionTokenFromRequest(r *http.Request, cookieName string) (string, bool) {
	if r == nil {
		return "", false
	}
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token, true
			}
		}
	}
	if strings.TrimSpace(cookieName) == "" {
		cookieName = v2SessionCookieNameFromEnv()
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(cookie.Value)
	return token, token != ""
}

func identityFromV2SessionClaims(claims *session.Claims) *identity.Identity {
	if claims == nil {
		return &identity.Identity{}
	}
	var roles []string
	for _, role := range strings.Split(claims.Role, ",") {
		if role = strings.TrimSpace(role); role != "" {
			roles = append(roles, role)
		}
	}
	return &identity.Identity{
		UserID: v2SessionOwnerID(claims),
		OrgID:  v2SessionTenantID(claims),
		Email:  strings.TrimSpace(claims.Email),
		Roles:  roles,
	}
}

func enrichV2SessionIdentityFromMembership(ctx context.Context, store controlplane.AuthStore, defaultTenant string, saasMode bool, claims *session.Claims, id *identity.Identity) *controlplane.Membership {
	if claims == nil {
		return nil
	}
	// Legacy SaaS sessions issued before the login-time tenant resolver carry
	// no tenant claim; prefer the user's org membership over the shared
	// default tenant so standalone resolves the same data as embedded.
	if saasMode && strings.TrimSpace(v2SessionTenantID(claims)) == "" {
		if membership := preferredOrgMembershipForIdentity(ctx, store, id); membership != nil {
			appendIdentityRole(id, membership.RoleKey)
			return membership
		}
	}
	return enrichIdentityFromMembership(ctx, store, id, v2SessionTenantID(claims), claims.TenantID, claims.OrgID, defaultTenant, "default")
}

func enrichIdentityFromMembership(ctx context.Context, store controlplane.AuthStore, id *identity.Identity, tenantIDs ...string) *controlplane.Membership {
	if store == nil || id == nil || !id.IsAuthenticated() || strings.TrimSpace(id.UserID) == "" {
		return nil
	}
	tenantIDs = uniqueNonEmptyStrings(tenantIDs...)
	if len(tenantIDs) == 0 {
		return nil
	}
	for _, tenantID := range tenantIDs {
		membership, err := store.GetMembership(ctx, tenantID, id.UserID)
		if err != nil || membership == nil {
			continue
		}
		status := strings.TrimSpace(membership.Status)
		if status != "" && !strings.EqualFold(status, "active") {
			continue
		}
		appendIdentityRole(id, membership.RoleKey)
		return membership
	}
	return nil
}

// applyOwnerTenantFallback resolves a SaaS identity without any tenant scope
// onto its own subject (owner tenant). This is the deterministic last rung
// shared by the gateway and browser-session paths: a user without an
// organization claim and without any control-plane membership resolves the
// SAME tenant on every surface (one-truth rule) instead of silently seeing
// the legacy path, the shared default tenant, or a fail-closed denial.
func applyOwnerTenantFallback(id *identity.Identity, saasMode bool) {
	if !saasMode || id == nil || !id.IsAuthenticated() {
		return
	}
	if strings.TrimSpace(id.OrgID) != "" {
		return
	}
	if userID := strings.TrimSpace(id.UserID); userID != "" {
		id.OrgID = canonicalOwnerTenantID(userID)
	}
}

func hydrateIdentityTenantFromMembership(id *identity.Identity, membership *controlplane.Membership) {
	if id == nil || membership == nil {
		return
	}
	if strings.TrimSpace(id.OrgID) != "" {
		return
	}
	if tenantID := strings.TrimSpace(membership.TenantID); tenantID != "" {
		id.OrgID = tenantID
	}
}

func appendIdentityRole(id *identity.Identity, role string) {
	if id == nil {
		return
	}
	role = strings.TrimSpace(role)
	if role == "" {
		return
	}
	for _, existing := range id.Roles {
		if strings.EqualFold(strings.TrimSpace(existing), role) {
			return
		}
	}
	id.Roles = append(id.Roles, role)
}

func uniqueNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// v2SessionOwnerID returns the canonical control-plane user id carried by the
// session subject. The OIDC subject is used directly as the user id (see
// v2CloudUserUpsert). The legacy "pocketbase:" prefix from pre-retirement
// sessions is stripped for backward compatibility during the session-cookie
// rollover window.
func v2SessionOwnerID(claims *session.Claims) string {
	if claims == nil {
		return ""
	}
	subject := strings.TrimSpace(claims.Subject)
	if localID, ok := strings.CutPrefix(subject, "pocketbase:"); ok {
		return strings.TrimSpace(localID)
	}
	return subject
}

func v2SessionTenantID(claims *session.Claims) string {
	if claims == nil {
		return ""
	}
	if subject := v2SessionOwnerID(claims); subject != "" && demoguard.IsDemoUser(subject) {
		if tenantID := demoguard.DemoTenantID(); tenantID != "" {
			return tenantID
		}
	}
	orgID := strings.TrimSpace(claims.OrgID)
	if orgID == "" {
		orgID = strings.TrimSpace(claims.TenantID)
	}
	return orgID
}

// v2CloudUserUpsert persists the authenticated cloud identity into the Postgres
// control plane (techstack_users + techstack_memberships) at login time,
// replacing the retired PocketBase users/user_links collections. The OIDC
// subject is the canonical user id; the membership carries the provider link
// (provider_key=cloud, subject_id=<oidc subject>). Auth is now a single
// Postgres-backed authority. The membership lands under the tenant resolved by
// the login-time TenantResolver; when that differs from the shared default
// tenant, the default membership is also kept for the session-cookie rollover
// window (legacy lookups still chain through it).
func v2CloudUserUpsert(authStore controlplane.AuthStore, defaultTenant string) commonauthflow.UserUpsert {
	if authStore == nil {
		return nil
	}
	fallbackTenant := strings.TrimSpace(defaultTenant)
	if fallbackTenant == "" {
		fallbackTenant = sharedDefaultTenantID
	}
	return func(ctx context.Context, claims *oidcclient.Claims, resolvedTenant string, _ string) error {
		if claims == nil {
			return nil
		}
		subject := strings.TrimSpace(claims.Subject)
		if subject == "" {
			return fmt.Errorf("cloud user subject is required")
		}
		email := strings.TrimSpace(claims.Email)
		if email == "" {
			return fmt.Errorf("cloud user email is required")
		}
		tenantID := strings.TrimSpace(resolvedTenant)
		if tenantID == "" {
			tenantID = fallbackTenant
		}
		const activeStatus = "active"
		tenantRecord := controlplane.Tenant{ID: tenantID}
		if tenantID != fallbackTenant {
			// Mirror ensureIdentityTenantProjection so a login never downgrades
			// an org/owner tenant to self_hosted defaults on the conflict path.
			tenantRecord = controlplane.Tenant{
				ID: tenantID, DisplayName: tenantID, Kind: "saas", Status: activeStatus,
			}
			if strings.HasPrefix(tenantID, orgTenantPrefix) {
				tenantRecord.ExternalOrgID = tenantID
			}
		}
		if _, err := authStore.UpsertTenant(ctx, tenantRecord); err != nil {
			return fmt.Errorf("upsert tenant: %w", err)
		}
		if _, err := authStore.UpsertUser(ctx, controlplane.User{
			ID:           subject,
			PrimaryEmail: email,
			DisplayName:  strings.TrimSpace(claims.Name),
		}); err != nil {
			return fmt.Errorf("upsert cloud user: %w", err)
		}
		membershipMetadata := map[string]any{
			"platform_roles": v2CloudPlatformRoles(claims),
		}
		if entitlements := v2CloudEntitlements(claims); len(entitlements) > 0 {
			membershipMetadata["entitlements"] = entitlements
		}
		roleKey := v2CloudMembershipRoleKey(claims)
		if _, err := authStore.UpsertMembership(ctx, controlplane.Membership{
			ID:          tenantID + ":" + subject,
			TenantID:    tenantID,
			UserID:      subject,
			RoleKey:     roleKey,
			ProviderKey: "cloud",
			SubjectID:   subject,
			Metadata:    membershipMetadata,
		}); err != nil {
			return fmt.Errorf("upsert cloud membership: %w", err)
		}
		if tenantID != fallbackTenant {
			if _, err := authStore.UpsertTenant(ctx, controlplane.Tenant{ID: fallbackTenant}); err != nil {
				return fmt.Errorf("upsert default tenant: %w", err)
			}
			if _, err := authStore.UpsertMembership(ctx, controlplane.Membership{
				ID:          fallbackTenant + ":" + subject,
				TenantID:    fallbackTenant,
				UserID:      subject,
				RoleKey:     roleKey,
				ProviderKey: "cloud",
				SubjectID:   subject,
				Metadata:    membershipMetadata,
			}); err != nil {
				return fmt.Errorf("upsert default cloud membership: %w", err)
			}
		}
		return nil
	}
}

// contextWithMembershipAuthorization projects server-owned membership grants
// into both product feature flags and the authorization context used by
// Inventory. Existing Edge-signed grants always win; the membership fallback
// is only used for a verified TechStack session whose grants were persisted
// server-side during the Auth0 callback.
func contextWithMembershipAuthorization(ctx context.Context, membership *controlplane.Membership) context.Context {
	if ctx == nil || membership == nil {
		return ctx
	}
	entitlements := stringSliceFromAny(membership.Metadata["entitlements"])
	if len(entitlements) == 0 {
		return ctx
	}
	if _, ok := commonedgeauth.FlagsFromContext(ctx); !ok {
		flags := make(map[string]bool, len(entitlements))
		for _, entitlement := range entitlements {
			if entitlement = strings.TrimSpace(entitlement); entitlement != "" {
				flags[entitlement] = true
			}
		}
		if len(flags) > 0 {
			ctx = commonedgeauth.FlagsToContext(ctx, commonedgeauth.FlagSet{Flags: flags})
		}
	}
	if _, ok := middleware.SignedEntitlementsFromContext(ctx); !ok {
		ctx = middleware.WithSignedEntitlements(ctx, entitlements...)
	}
	return ctx
}

func v2CloudEntitlements(claims *oidcclient.Claims) []string {
	if claims == nil {
		return nil
	}
	for _, key := range []string{"https://kombify.io/entitlements", "entitlements"} {
		if entitlements := stringSliceFromAny(claims.Raw[key]); len(entitlements) > 0 {
			return entitlements
		}
	}
	return nil
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func v2CloudMembershipRoleKey(claims *oidcclient.Claims) string {
	roles := v2CloudPlatformRoles(claims)
	if len(roles) == 0 || roles[0] == "" || roles[0] == commonrole.User.String() {
		return "member"
	}
	return roles[0]
}

func v2CloudPlatformRoles(claims *oidcclient.Claims) []string {
	if claims == nil {
		return nil
	}
	roles := commonrole.ExtractAllRolesFromClaims(claims.Raw, "")
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if s := strings.TrimSpace(role.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func bindCSRF(router *httpx.Router, cfg *config.Config) {
	csrfCfg := middleware.DefaultCSRFConfig()
	csrfCfg.Secure = cfg.IsProduction()
	csrfCfg.IgnorePaths = append(csrfCfg.IgnorePaths,
		"/api/v1/health/live",
		"/api/v1/health/ready",
		"/api/v1/health/startup",
		"/api/v1/openapi.yaml",
		localDeviceSessionPath,
		"/live",
		"/ready",
		"/startup",
	)
	csrfCfg.IgnorePrefixes = append(csrfCfg.IgnorePrefixes,
		"/api/v1/agent/",
		"/api/v1/internal/",
	)
	csrf := middleware.NewCSRFMiddleware(csrfCfg)
	router.BindFunc(csrf.HTTPXMiddleware())
	router.GET("/api/v1/csrf", func(e *httpx.Event) error {
		middleware.CSRFTokenEndpoint(csrfCfg.Secure)(e.Response, e.Request)
		return nil
	})
}

func bindCORS(router *httpx.Router, cfg *config.Config) {
	corsOrigins, corsWarnings := cfg.ValidateCORSOrigins()
	for _, warning := range corsWarnings {
		fmt.Printf("⚠️  CORS: %s\n", warning)
	}
	// local is prod-hard for CORS: only explicitly configured origins, no
	// dev-default merge.
	if !cfg.IsProduction() && !cfg.IsLocal() {
		corsOrigins = mergeCORSOrigins(config.DefaultCORSOrigins, corsOrigins)
	}
	router.BindFunc(httpx.CORS(httpx.CORSConfig{
		AllowOrigins:     corsOrigins,
		AllowMethods:     []string{"GET", "HEAD", "PUT", "PATCH", "POST", "DELETE"},
		AllowHeaders:     []string{"Accept", "Accept-Language", "Content-Type", "Authorization", "X-CSRF-Token", "X-SSH-Fingerprint", localDeviceSessionHeader},
		AllowCredentials: !hasWildcardOrigin(corsOrigins),
		ExposeHeaders:    []string{"X-CSRF-Token", "X-Request-ID", "X-Correlation-ID", "X-Trace-ID"},
		MaxAge:           600,
	}))
}

func mergeCORSOrigins(groups ...[]string) []string {
	out := make([]string, 0)
	for _, group := range groups {
		for _, origin := range group {
			if normalized, ok := normalizeCORSOrigin(origin); ok && !containsOrigin(out, normalized) {
				out = append(out, normalized)
			}
		}
	}
	return out
}

// normalizeCORSOrigin trims and sanitizes a single CORS origin. The wildcard
// "*" is preserved verbatim; any other value is run through SanitizeOrigin.
// The bool is false when the origin is empty or fails sanitization.
func normalizeCORSOrigin(origin string) (string, bool) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return "", false
	}
	if origin == "*" {
		return origin, true
	}
	origin = config.SanitizeOrigin(origin)
	if origin == "" {
		return "", false
	}
	return origin, true
}

func containsOrigin(origins []string, candidate string) bool {
	for _, origin := range origins {
		if origin == candidate {
			return true
		}
	}
	return false
}

func hasWildcardOrigin(origins []string) bool {
	for _, origin := range origins {
		if strings.TrimSpace(origin) == "*" {
			return true
		}
	}
	return false
}

func bindRateLimiter(router *httpx.Router, cfg *config.Config) {
	rateLimiter := middleware.NewRateLimiter(cfg.Server.RateLimitRPS, cfg.Server.RateLimitBurst)
	portalRate := cfg.Server.RateLimitRPS
	if portalRate < 2 {
		portalRate = 2
	}
	portalBurst := cfg.Server.RateLimitBurst
	if portalBurst < 8 {
		portalBurst = 8
	}
	portalExchangeLimiter := middleware.NewRateLimiter(portalRate, portalBurst)
	router.BindFunc(func(e *httpx.Event) error {
		if portalExchangeRequest(e.Request) {
			if !portalExchangeLimiter.Allow(middleware.RequestRateLimitKey(e.Request)) {
				e.Response.Header().Set("Retry-After", "1")
				return httpx.Error(e, 429, ksapi.ErrCodeRateLimited, "Too many requests. Please slow down and try again.", nil)
			}
			return e.Next()
		}
		if rateLimitBypassEligible(e.Request) {
			return e.Next()
		}
		if !rateLimiter.Allow(middleware.RequestRateLimitKey(e.Request)) {
			e.Response.Header().Set("Retry-After", "1")
			return httpx.Error(e, 429, ksapi.ErrCodeRateLimited, "Too many requests. Please slow down and try again.", nil)
		}
		return e.Next()
	})
}

func rateLimitBypassEligible(r *http.Request) bool {
	if frontendFallbackEligible(r) {
		return true
	}
	if r == nil {
		return false
	}
	// The embedded portal exchange verifies a short-lived, signed kombify Cloud
	// token in its handler. It must not share the anonymous page-bootstrap
	// bucket: a normal iframe loads enough state in parallel to exhaust that
	// bucket before its first authenticated session can be established.
	if portalExchangeRequest(r) {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/auth/mode", "/api/v1/auth/stack-identity", "/api/v1/client/bootstrap", "/api/v2/whoami", "/api/v2/auth/providers":
		return true
	default:
		return false
	}
}

func portalExchangeRequest(r *http.Request) bool {
	return r != nil && r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/portal-verify"
}

func printStartup(addr string, deps routeDeps) {
	fmt.Printf("🚀 kombifyTechstack v%s - The Hybrid Infrastructure Unifier\n", deps.version)
	fmt.Printf("   API:            http://%s/api/\n", addr)
	fmt.Printf("   V2 Health:      http://%s/api/v2/health\n", addr)
	fmt.Printf("   Health:         http://%s/api/v1/health\n", addr)
	fmt.Printf("   Health (k8s):   http://%s/api/v1/health/live, /ready\n", addr)
	fmt.Printf("   OpenAPI:        http://%s/api/v1/openapi.yaml\n", addr)
	fmt.Printf("   Capabilities:   http://%s/api/v1/capabilities\n", addr)
	fmt.Printf("   Instance:       http://%s/api/v1/instance (id=%s)\n", addr, deps.startup.identity.ID)
	fmt.Printf("   Metrics:        http://%s/metrics\n", addr)
	fmt.Printf("   Unifier:        http://%s/api/v1/unifier/\n", addr)
	fmt.Printf("   Discovery:      http://%s/api/v1/discovery/\n", addr)
	fmt.Printf("   Trust:          http://%s/api/v1/trust/\n", addr)
	fmt.Printf("   Agents:         http://%s/api/v1/agents\n", addr)
	fmt.Printf("   System Accts:   http://%s/api/v1/system-accounts/{role}/reset\n", addr)
	fmt.Printf("   Backups:        http://%s/api/v1/backups\n", addr)
	fmt.Printf("   Features:       http://%s/api/v1/features\n", addr)
	fmt.Printf("   Monitoring:     http://%s/api/v1/monitor/\n", addr)
	fmt.Printf("   Tunnel:         http://%s/api/v1/tunnel/\n", addr)
}

func registerPocketBaseBootstrap(app *pocketbase.PocketBase) {
	if err := pbmigration.EnsureSaaSAuthCollections(app); err != nil {
		fmt.Printf("⚠️  SaaS auth collection bootstrap warning: %v\n", err)
	}
	if err := hooks.BootstrapUsers(app); err != nil {
		fmt.Printf("⚠️  User bootstrap warning: %v\n", err)
	}
	if err := hooks.BootstrapOAuthProviders(app); err != nil {
		fmt.Printf("⚠️  OAuth2 provider bootstrap warning: %v\n", err)
	}
}

func registerBusinessHooks(app *pocketbase.PocketBase, cfg *config.Config, log *logger.Logger) error {
	tenantEnforcer := tenant.NewEnforcer(cfg.DeploymentMode)
	hooks.RegisterStackHooks(app, tenantEnforcer)
	if err := hooks.RegisterWalletHooks(app, log, cfg.DeploymentMode); err != nil {
		log.Error("wallet_hooks_failed", "error", err)
		return err
	}
	hooks.RegisterWorkerHooks(app, tenantEnforcer)
	return nil
}

// runTenantBackfillCheck performs the SaaS tenant-id backfill repair and
// consistency check once at startup against the bootstrapped data layer. It is
// the direct-call replacement for the previously OnServe-bound check.
func runTenantBackfillCheck(app *pocketbase.PocketBase, cfg *config.Config, log *logger.Logger) error {
	if !cfg.DeploymentMode.IsSaaS() {
		return nil
	}
	repair, repairErr := tenant.BackfillStackScopedTenantIDs(app, cfg.DeploymentMode)
	if repairErr != nil {
		log.Error("tenant_backfill_repair_failed", "error", repairErr)
		return repairErr
	}
	if repair != nil && (repair.TotalUpdated() > 0 || repair.TotalSkipped() > 0) {
		log.Info("tenant_backfill_repair_completed",
			"updated", repair.Updated,
			"skipped", repair.Skipped,
		)
	}

	report, err := tenant.CheckBackfill(app, cfg.DeploymentMode)
	if err != nil {
		issues := []string(nil)
		if report != nil {
			issues = report.FatalIssueSummaries()
		}
		log.Error("tenant_backfill_check_failed", "issues", issues, "error", err)
		return err
	}
	if report != nil && len(report.Issues) > 0 {
		log.Warn("tenant_backfill_check_warn", "issues", report.IssueSummaries())
		return nil
	}
	log.Info("tenant_backfill_check_passed", "collections", len(report.Checked))
	return nil
}
