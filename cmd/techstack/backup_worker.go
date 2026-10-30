package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/featureflags"
	"github.com/kombifyio/techstack/internal/backupjobs"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	storageauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/releasechannel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
)

const backupWorkerModeEnv = "TECHSTACK_BACKUP_WORKER_MODE"

type backupWorkerRuntime struct {
	database     *db.DB
	orch         *orchestrator.Orchestrator
	scanner      *backupjobs.Scanner
	entitlements *featureflags.Client
}

// The same binary has no additional listener. Stage composes this background
// lifecycle in the existing private Execution process, where the genuine
// sealed owner POST activates the existing process-local Stage policy.
func openBackupWorker(ctx context.Context, inExecution bool) (*backupWorkerRuntime, error) {
	mode := strings.TrimSpace(os.Getenv(backupWorkerModeEnv))
	if inExecution && mode != "execution" || !inExecution && mode != "standalone" {
		return nil, errors.New("backup worker requires its explicit private process profile")
	}
	if config.ReleaseChannel() != "stable" && (!inExecution || config.ReleaseChannel() != "preview" || !releasechannel.StageRuntimePrepared()) {
		return nil, errors.New("Preview backup work requires the existing prepared private Execution owner")
	}
	dsn := strings.TrimSpace(os.Getenv(db.EnvBackupWorkerDatabaseURL))
	parsed, err := pgx.ParseConfig(dsn)
	if dsn == "" || err != nil || parsed.User != db.BackupWorkerRole || parsed.Password == "" ||
		parsed.TLSConfig == nil || parsed.TLSConfig.InsecureSkipVerify || len(parsed.Fallbacks) != 0 {
		return nil, errors.New("backup worker requires its distinct verified-transport database custody")
	}
	for key := range parsed.RuntimeParams {
		if key == "role" || key == "session_authorization" || key == "options" {
			return nil, errors.New("backup worker refuses session authority overrides")
		}
	}
	if os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv(db.EnvAllowDBGenesis) != "false" || os.Getenv(db.EnvMigrationDatabaseURL) != "" {
		return nil, errors.New("backup worker requires verify-only database custody")
	}
	if strings.TrimSpace("") != "" {
		return nil, errors.New("private backup worker requires a finite process projection")
	}
	if !inExecution {
		for _, name := range []string{"DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL", "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_KEY", "TECHSTACK_DB_CONTEXT_KEY"} {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				return nil, errors.New("standalone backup worker refuses application, issuer and provider custody")
			}
		}
	}
	if db.DeclaresStagingDatabaseBinding() && (!inExecution || !releasechannel.StageRuntimePrepared()) {
		return nil, errors.New("Stage backup work cannot borrow a Stable process profile")
	}
	pool, err := db.OpenContext(ctx, db.Config{Backend: db.StoreBackendPostgres, DriverName: db.PostgresDriverName,
		DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		return nil, errors.New("backup worker database authentication failed")
	}
	refuse := func(err error) (*backupWorkerRuntime, error) { pool.Close(); return nil, err }
	if err := db.VerifyBackupWorkerRuntimeRole(ctx, pool.DB); err != nil {
		return refuse(err)
	}
	if err := db.VerifyExpectedDatabaseIdentity(ctx, pool.DB); err != nil {
		return refuse(err)
	}
	owner, err := db.ApplicationAuthorityOwner(ctx, pool.DB)
	if err != nil {
		return refuse(err)
	}
	if err := db.VerifyApplicationPrivileges(ctx, pool.DB, db.ApplicationPrincipal{Role: db.BackupWorkerRole, AuthorityOwner: owner}, true); err != nil {
		return refuse(err)
	}
	if err := pool.VerifyMigrations(ctx); err != nil {
		return refuse(err)
	}
	encryptor, err := storageauth.NewSecretEncryptor([]byte(os.Getenv("TECHSTACK_ENCRYPTION_KEY")))
	if err != nil {
		return refuse(errors.New("backup worker requires the retained backup wrapping custody"))
	}
	schedules, err := backupjobs.NewStore(pool.DB)
	if err != nil {
		return refuse(err)
	}
	usage, err := backupstore.NewPostgresUsageStore(pool.DB)
	if err != nil {
		return refuse(err)
	}
	custody, err := backupstore.NewPostgresCustodyStore(pool.DB, encryptor)
	if err != nil {
		return refuse(err)
	}
	projection, err := openBackupEntitlements(ctx)
	if err != nil {
		return refuse(err)
	}
	admission := func(ctx context.Context, req jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
		// The current locked schedule and canonical stack supply the owner. The
		// canonical platform projection resolves its current subscription itself;
		// never construct tier/role claims or reuse a request's expired flags.
		decision, err := readBackupEntitlements(ctx, projection, req.UserID, req.IncludeContent)
		if err != nil {
			return jobs.BackupAdmissionDecision{Denied: true}, err
		}
		gate, err := backupjobs.NewAdmission(backupjobs.AdmissionConfig{
			Features: decision,
			Usage:    backupjobs.CurrentUsage{Custody: custody, Store: usage},
		})
		if err != nil {
			return jobs.BackupAdmissionDecision{Denied: true}, err
		}
		return jobs.AdmitBackup(ctx, gate, req)
	}
	store := controlplane.NewPostgresStore(pool.DB, controlplane.WithBackupWorkerConflictProjection())
	cfg := orchestrator.DefaultConfig()
	cfg.Workers = 1
	cfg.StackStore, cfg.JobStore, cfg.WorkerStore = store, store, store
	cfg.StackKitCommander = agentcontrol.NewHub(pool.DB)
	cfg.JobTypeFilter = func(kind jobs.JobType) bool { return kind == jobs.JobTypeBackup }
	orch := orchestrator.New(cfg, logger.Default())
	onDenied := func(due backupjobs.Due, decision jobs.BackupAdmissionDecision) {
		logger.Default().Info("backup_scan_denied", "tenant_id", due.TenantID, "stack_id", due.StackID, "reason_code", decision.Details["reason_code"])
	}
	scanner, err := backupjobs.NewScanner(backupjobs.ScannerConfig{Store: schedules, Admission: admission, Enqueuer: orch,
		DispatchDue: func(ctx context.Context, due backupjobs.Due, now time.Time) error {
			return schedules.DispatchDue(ctx, due, now, admission, orch.ReserveBackupTx, onDenied)
		},
		OnError: func(err error) { logger.Default().Error("backup_worker_pass_failed", "error", err) }, OnDenied: onDenied})
	if err != nil {
		projection.Close()
		orch.Stop()
		return refuse(err)
	}
	return &backupWorkerRuntime{database: pool, orch: orch, scanner: scanner, entitlements: projection}, nil
}

func (worker *backupWorkerRuntime) start(ctx context.Context) func() {
	background := newBackgroundRuntimeLifecycle(ctx)
	worker.orch.Start()
	background.run(worker.scanner.Run)
	background.run(worker.orch.RunPendingJobWake)
	return func() { background.stopAndWait(); worker.orch.Stop() }
}

func runBackupWorker(ctx context.Context) error {
	worker, err := openBackupWorker(ctx, false)
	if err != nil {
		return err
	}
	defer worker.database.Close()
	defer worker.entitlements.Close()
	signalCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stop := worker.start(signalCtx)
	defer stop()
	<-signalCtx.Done()
	return nil
}

const backupEntitlementsDSNEnv = "TECHSTACK_BACKUP_ENTITLEMENTS_DATABASE_URL"
const backupEntitlementsIdentityEnv = "TECHSTACK_BACKUP_ENTITLEMENTS_DATABASE_IDENTITY"
const backupEntitlementsRole = "techstack_backup_entitlements"

// The existing Common reader is used directly, without its HTTP compatibility
// endpoint or stale flag cache. Every answer comes from the canonical SQL
// evaluator's EffectiveEnabled, including revocation and current subject tier.
type backupProjectionDecision struct {
	subject string
	enabled map[string]bool
}

// Only these value-free reasons may cross the native qualification boundary.
// Never retain the underlying SQL, transport or decoder error in this error.
type backupEntitlementsError string

func (reason backupEntitlementsError) Error() string { return string(reason) }

func backupEntitlementsReadError(err error) backupEntitlementsError {
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return "ENTITLEMENTS_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "ENTITLEMENTS_CANCELED"
	}
	var sqlError *pgconn.PgError
	if errors.As(err, &sqlError) && regexp.MustCompile(`^[A-Z0-9]{5}$`).MatchString(sqlError.Code) {
		return backupEntitlementsError("ENTITLEMENTS_SQLSTATE_" + sqlError.Code)
	}
	var scanError pgx.ScanArgError
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &scanError) || errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		return "ENTITLEMENTS_DECODE"
	}
	return "ENTITLEMENTS_READ_FAILED"
}

func (decision backupProjectionDecision) IsEnabled(_ context.Context, key, user string) (bool, error) {
	value, found := decision.enabled[key]
	if decision.subject == "" || user != decision.subject || !found {
		return false, errors.New("backup projection owner or feature denied")
	}
	return value, nil
}

func readBackupEntitlements(ctx context.Context, client *featureflags.Client, user string, includeContent bool) (backupProjectionDecision, error) {
	decision := backupProjectionDecision{subject: user, enabled: map[string]bool{}}
	if client == nil || user == "" {
		return decision, backupEntitlementsError("ENTITLEMENTS_UNAVAILABLE")
	}
	rows, err := client.GetEntitlements(ctx, featureflags.EvaluationContext{
		UserID: user, SubjectID: user, TargetingKey: user, Mode: "saas_standalone", DeploymentChannel: config.ReleaseChannel(),
	})
	if err != nil {
		return decision, backupEntitlementsReadError(err)
	}
	if len(rows) > 500 {
		return decision, backupEntitlementsError("ENTITLEMENTS_ROW_LIMIT")
	}
	for _, key := range monthlyruntime.RequiredFeatureKeysForBackup(includeContent) {
		found, enabled := false, false
		for _, row := range rows {
			if row.FeatureKey != key {
				continue
			}
			if row.Tool != "kombify-techstack" || row.Revision == "" || (found && enabled != row.EffectiveEnabled) {
				return decision, backupEntitlementsError("ENTITLEMENTS_INCONSISTENT")
			}
			found, enabled = true, row.EffectiveEnabled
		}
		if !found {
			return decision, backupEntitlementsError("ENTITLEMENTS_ABSENT")
		}
		decision.enabled[key] = enabled
	}
	return decision, nil
}

func openBackupEntitlements(ctx context.Context) (*featureflags.Client, error) {
	raw := strings.TrimSpace(os.Getenv(backupEntitlementsDSNEnv))
	cfg, err := pgxpool.ParseConfig(raw)
	if raw == "" || err != nil || cfg.ConnConfig.User != backupEntitlementsRole || cfg.ConnConfig.Password == "" ||
		cfg.ConnConfig.Database != "kombify" || cfg.ConnConfig.TLSConfig == nil || cfg.ConnConfig.TLSConfig.InsecureSkipVerify || len(cfg.ConnConfig.Fallbacks) != 0 {
		return nil, errors.New("backup worker requires its separate verified platform projection reader")
	}
	for key := range cfg.ConnConfig.RuntimeParams {
		if key != "application_name" && key != "connect_timeout" && key != "channel_binding" {
			return nil, errors.New("backup projection refuses session authority overrides")
		}
	}
	expected := strings.TrimSpace(os.Getenv(backupEntitlementsIdentityEnv))
	if !strings.HasPrefix(expected, "kombify@") || len(strings.TrimPrefix(expected, "kombify@")) < 10 {
		return nil, errors.New("backup projection requires its physical database identity")
	}
	if config.ReleaseChannel() == "preview" && (expected != "kombify@7699436836349883199" || cfg.ConnConfig.Host != "kombify-stg-pg-8146d168.pgv20v39ze9j4j3d3dwbj6p979.c0.eu-central-1.aws.pg.clickhouse.cloud") {
		return nil, errors.New("Stage backup projection cannot receive production custody")
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "3000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("backup projection connection refused")
	}
	defer pool.Close()
	var identity string
	var safe bool
	err = pool.QueryRow(ctx, `SELECT current_database()||'@'||system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&identity)
	if err != nil || identity != expected {
		return nil, errors.New("backup projection physical binding denied")
	}
	// Audit the exact Common GetEntitlements dependency closure, including RLS
	// helpers and default evaluation. PUBLIC and inherited column ACLs count;
	// read-only transactions alone do not establish a finite authority boundary.
	// The two FOR ALL TO PUBLIC admin-write policies on the entitlement tables call
	// is_entitlement_admin_writer(), so PostgreSQL needs EXECUTE on it for any read
	// there; the reader must hold it, and only while it stays that invoker-only
	// predicate with the reviewed body of Cloud migration 000134 (md5 of prosrc).
	err = pool.QueryRow(ctx, `WITH required(signature, definer, language, volatility) AS (VALUES
  ('public.get_entitlements(text)',false,'sql','s'),
  ('public.get_runtime_feature_flags(text)',false,'sql','s'),
  ('public.is_entitlement_enabled(text,text)',false,'sql','s'),
  ('public.runtime_feature_role_budgets(text,text)',true,'sql','s'),
  ('public.runtime_entitlement_default_enabled(text,text)',false,'sql','s'),
  ('public.runtime_entitlement_user_setting_enabled(text)',false,'sql','s'),
  ('public.runtime_entitlement_target_matches(text,boolean,boolean,boolean,boolean)',false,'sql','i'),
  ('public.runtime_entitlement_release_matches(text,boolean)',false,'sql','s'),
  ('public.current_entitlement_context_values(text)',false,'sql','s'),
  ('public.resolve_platform_subject_to_tier(text)',true,'plpgsql','s'),
  ('public.is_entitlement_runtime_role()',false,'sql','s')
), expected AS (
  SELECT required.*,to_regprocedure(signature)::oid AS oid FROM required
), common_required(signature,definer,language) AS (VALUES
  ('kombify_ctx.subject()',false,'sql'),('kombify_ctx.org_id()',false,'sql'),
  ('kombify_ctx.verified_context()',true,'plpgsql')
), common_expected AS (SELECT common_required.*,to_regprocedure(signature)::oid AS oid FROM common_required),
policy_predicate AS (SELECT f.oid FROM pg_proc f JOIN pg_roles owner ON owner.oid=f.proowner JOIN pg_language l ON l.oid=f.prolang
  WHERE f.oid=to_regprocedure('public.is_entitlement_admin_writer()')::oid AND f.prokind='f' AND NOT f.prosecdef
    AND f.provolatile='s' AND l.lanname='sql' AND owner.rolname='kombify_user'
    AND f.proconfig IS NOT DISTINCT FROM ARRAY['search_path=public']::text[] AND md5(f.prosrc)='fa3ba695269aa2dfc8f59f1ce39340d7'),
principals AS (SELECT * FROM pg_roles WHERE rolname IN ('techstack_backup_entitlements','kombify_entitlements_runtime')),
code_owner AS (SELECT * FROM pg_roles WHERE rolname='kombify_entitlements_code_owner'),
code_owner_default_acl AS (
  SELECT d.oid FROM pg_catalog.pg_default_acl d
  WHERE d.defaclrole='kombify_entitlements_code_owner'::regrole AND d.defaclnamespace=0 AND d.defaclobjtype='f'
    AND cardinality(d.defaclacl)=1 AND EXISTS(SELECT 1 FROM aclexplode(d.defaclacl) a
      WHERE a.grantee=d.defaclrole AND a.grantor=d.defaclrole AND a.privilege_type='EXECUTE' AND NOT a.is_grantable)
),
owner_columns(relation_name,column_name) AS (VALUES
  ('users','id'),('users','role'),('users','auth_subject'),
  ('subscriptions','user_id'),('subscriptions','plan'),('subscriptions','status'),('subscriptions','current_period_end'),('subscriptions','created_at'),
  ('entitlement_revisions','id'),('entitlement_revisions','source'),('entitlement_revisions','is_current'),('entitlement_revisions','revision'),
  ('entitlement_grants','subject_key'),('entitlement_grants','token_limit'),('entitlement_grants','revision_id'),
  ('entitlement_grants','feature_id'),('entitlement_grants','grant_kind'),('entitlement_grants','enabled'),('entitlement_grants','subject_kind'))
SELECT r.rolname='techstack_backup_entitlements' AND current_user=session_user AND current_user=r.rolname AND current_setting('transaction_read_only')='on'
  AND r.rolcanlogin AND r.rolinherit AND (SELECT count(*)=2 FROM principals)
  AND NOT EXISTS(SELECT 1 FROM principals p WHERE p.rolsuper OR p.rolcreatedb OR p.rolcreaterole OR p.rolreplication OR p.rolbypassrls)
  AND EXISTS(SELECT 1 FROM principals g WHERE g.rolname='kombify_entitlements_runtime' AND NOT g.rolcanlogin)
  AND NOT EXISTS(SELECT 1 FROM pg_auth_members m JOIN principals p ON p.oid=m.member
    WHERE p.rolname='kombify_entitlements_runtime' OR m.roleid<>'kombify_entitlements_runtime'::regrole
      OR m.admin_option OR m.set_option OR NOT m.inherit_option)
  AND EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid
    AND m.roleid='kombify_entitlements_runtime'::regrole AND m.inherit_option AND NOT m.set_option AND NOT m.admin_option)
  AND NOT EXISTS(SELECT 1 FROM pg_shdepend d JOIN principals p ON p.oid=d.refobjid
    WHERE d.refclassid='pg_authid'::regclass AND d.deptype='o')
  AND NOT has_database_privilege(r.oid,current_database(),'CREATE,TEMP,CONNECT WITH GRANT OPTION,CREATE WITH GRANT OPTION,TEMP WITH GRANT OPTION')
  AND NOT EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname!~'^(pg_|information_schema$)'
    AND has_schema_privilege(r.oid,n.oid,'CREATE,USAGE WITH GRANT OPTION,CREATE WITH GRANT OPTION'))
  AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname!~'^(pg_|information_schema$)' AND c.relkind IN ('r','p','v','m','f') AND (
      has_table_privilege(r.oid,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
      OR CASE WHEN current_setting('server_version_num')::int>=170000
        THEN has_table_privilege(r.oid,c.oid,'MAINTAIN,MAINTAIN WITH GRANT OPTION') ELSE false END
      OR has_any_column_privilege(r.oid,c.oid,'INSERT,UPDATE,REFERENCES')
      OR has_table_privilege(r.oid,c.oid,'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,DELETE WITH GRANT OPTION,TRUNCATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION,TRIGGER WITH GRANT OPTION')
      OR has_any_column_privilege(r.oid,c.oid,'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION')
      OR ((has_table_privilege(r.oid,c.oid,'SELECT') OR has_any_column_privilege(r.oid,c.oid,'SELECT'))
        AND NOT (n.nspname='public' AND c.relname IN ('entitlement_revisions','entitlement_grants','entitlement_user_settings',
          'runtime_entitlement_grants','runtime_entitlement_user_settings','feature_definitions'))
        AND NOT (n.nspname='kombify_ctx' AND c.relname='role_class'))))
  AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname!~'^(pg_|information_schema$)' AND CASE WHEN c.relkind='S'
      THEN has_sequence_privilege(r.oid,c.oid,'USAGE,SELECT,UPDATE') ELSE false END)
  AND (SELECT count(*)=11 AND bool_and(f.prokind='f' AND f.prosecdef=e.definer
      AND f.provolatile::text=e.volatility AND l.lanname=e.language
      AND CASE WHEN e.definer THEN owner.rolname='kombify_entitlements_code_owner'
        AND NOT (owner.rolcanlogin OR owner.rolinherit OR owner.rolsuper OR owner.rolbypassrls OR owner.rolcreaterole OR owner.rolcreatedb OR owner.rolreplication)
        AND f.proconfig IS NOT DISTINCT FROM ARRAY['search_path=public, pg_temp']::text[]
      ELSE owner.rolname='kombify_user' AND NOT (owner.rolsuper OR owner.rolbypassrls OR owner.rolcreatedb OR owner.rolreplication)
        AND f.proconfig IS NULL END AND has_function_privilege(r.oid,f.oid,'EXECUTE'))
    FROM expected e JOIN pg_proc f ON f.oid=e.oid JOIN pg_roles owner ON owner.oid=f.proowner JOIN pg_language l ON l.oid=f.prolang)
  AND (SELECT count(*)=3 AND bool_and(f.prokind='f' AND f.prosecdef=e.definer AND f.provolatile='s'
      AND l.lanname=e.language AND owner.rolname='kombify_user'
      AND NOT (owner.rolsuper OR owner.rolbypassrls OR owner.rolcreatedb OR owner.rolreplication)
      AND f.proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog, pg_temp']::text[]
      AND has_function_privilege(r.oid,f.oid,'EXECUTE'))
    FROM common_expected e JOIN pg_proc f ON f.oid=e.oid JOIN pg_roles owner ON owner.oid=f.proowner JOIN pg_language l ON l.oid=f.prolang)
  AND NOT EXISTS(SELECT 1 FROM pg_proc f JOIN pg_namespace n ON n.oid=f.pronamespace
    WHERE n.nspname!~'^(pg_|information_schema$)' AND (
      has_function_privilege(r.oid,f.oid,'EXECUTE WITH GRANT OPTION') OR
      (has_schema_privilege(r.oid,n.oid,'USAGE') AND has_function_privilege(r.oid,f.oid,'EXECUTE')
        AND NOT EXISTS(SELECT 1 FROM expected e WHERE e.oid=f.oid)
        AND NOT EXISTS(SELECT 1 FROM common_expected e WHERE e.oid=f.oid)
        AND NOT EXISTS(SELECT 1 FROM policy_predicate e WHERE e.oid=f.oid))))
  AND (SELECT count(*)=1 AND bool_and(has_function_privilege(r.oid,e.oid,'EXECUTE')) FROM policy_predicate e)
  AND has_function_privilege(r.oid,'pg_catalog.pg_control_system()','EXECUTE')
  AND NOT has_function_privilege(r.oid,'pg_catalog.pg_control_system()','EXECUTE WITH GRANT OPTION')
  AND (SELECT count(*)=2 AND bool_and(c.relkind='v' AND (c.reloptions @> ARRAY['security_invoker=true']::text[]) IS TRUE)
    FROM pg_class c WHERE c.oid IN ('public.runtime_entitlement_grants'::regclass,'public.runtime_entitlement_user_settings'::regclass))
  AND (SELECT count(*)=2 AND bool_and(c.relrowsecurity AND c.relforcerowsecurity)
    FROM pg_class c WHERE c.oid IN ('public.subscriptions'::regclass,'public.entitlement_grants'::regclass))
  AND (SELECT count(*)=2 FROM pg_policy p WHERE p.polname='backup_projection_code_owner_read'
    AND p.polrelid IN ('public.subscriptions'::regclass,'public.entitlement_grants'::regclass)
    AND p.polcmd='r' AND p.polpermissive AND p.polroles=ARRAY['kombify_entitlements_code_owner'::regrole::oid]
    AND pg_get_expr(p.polqual,p.polrelid)='true' AND p.polwithcheck IS NULL)
  AND EXISTS(SELECT 1 FROM pg_class WHERE oid='kombify_ctx.role_class'::regclass AND relowner='kombify_user'::regrole AND relkind='r')
  AND (SELECT count(*)=1 AND bool_and(NOT (o.rolcanlogin OR o.rolinherit OR o.rolsuper OR o.rolbypassrls OR o.rolcreaterole OR o.rolcreatedb OR o.rolreplication)) FROM code_owner o)
  AND NOT EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member='kombify_entitlements_code_owner'::regrole)
  -- PG16+ producer posture: bootstrap creator grant is ADMIN-only, the trusted
  -- migration owner's self-grant is INHERIT-only; neither row currently grants SET.
  AND EXISTS(SELECT 1 FROM pg_roles WHERE oid=10 AND rolsuper)
  AND (SELECT count(*)=2 AND bool_and(m.member='kombify_user'::regrole AND NOT m.set_option)
      AND count(*) FILTER (WHERE m.grantor=10 AND m.admin_option AND NOT m.inherit_option)=1
      AND count(*) FILTER (WHERE m.grantor='kombify_user'::regrole AND m.inherit_option AND NOT m.admin_option)=1
    FROM pg_auth_members m WHERE m.roleid='kombify_entitlements_code_owner'::regrole)
  AND NOT EXISTS(SELECT 1 FROM pg_shdepend d WHERE d.refclassid='pg_authid'::regclass
    AND d.refobjid='kombify_entitlements_code_owner'::regrole AND d.deptype='o'
    AND NOT (d.dbid=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()) AND d.objsubid=0
      AND ((d.classid='pg_catalog.pg_proc'::regclass AND EXISTS(SELECT 1 FROM expected e WHERE e.oid=d.objid AND e.definer))
        OR (d.classid='pg_catalog.pg_default_acl'::regclass AND EXISTS(SELECT 1 FROM code_owner_default_acl a WHERE a.oid=d.objid)))))
  AND (SELECT count(*)=1 FROM code_owner_default_acl)
  AND NOT has_database_privilege('kombify_entitlements_code_owner',current_database(),'CREATE,TEMP')
  AND NOT EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname!~'^(pg_|information_schema$)'
    AND has_schema_privilege('kombify_entitlements_code_owner',n.oid,'CREATE,USAGE WITH GRANT OPTION,CREATE WITH GRANT OPTION'))
  AND NOT EXISTS(SELECT 1 FROM owner_columns c WHERE NOT has_column_privilege('kombify_entitlements_code_owner',to_regclass('public.'||c.relation_name),c.column_name,'SELECT'))
  AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname!~'^(pg_|information_schema$)' AND CASE WHEN c.relkind='S' THEN
      has_sequence_privilege('kombify_entitlements_code_owner',c.oid,'USAGE,SELECT,UPDATE')
    WHEN c.relkind IN ('r','p','v','m','f') THEN
      (has_table_privilege('kombify_entitlements_code_owner',c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
       OR has_any_column_privilege('kombify_entitlements_code_owner',c.oid,'INSERT,UPDATE,REFERENCES')
       OR has_table_privilege('kombify_entitlements_code_owner',c.oid,'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,DELETE WITH GRANT OPTION,TRUNCATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION,TRIGGER WITH GRANT OPTION')
       OR has_any_column_privilege('kombify_entitlements_code_owner',c.oid,'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION')
       OR CASE WHEN current_setting('server_version_num')::int>=170000 THEN has_table_privilege('kombify_entitlements_code_owner',c.oid,'MAINTAIN,MAINTAIN WITH GRANT OPTION') ELSE false END
       OR (NOT (n.nspname='kombify_ctx' AND c.relname='role_class') AND has_table_privilege('kombify_entitlements_code_owner',c.oid,'SELECT'))
       OR EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
         AND has_column_privilege('kombify_entitlements_code_owner',c.oid,a.attnum,'SELECT')
         AND NOT (n.nspname='kombify_ctx' AND c.relname='role_class')
         AND NOT EXISTS(SELECT 1 FROM owner_columns permitted WHERE n.nspname='public' AND permitted.relation_name=c.relname AND permitted.column_name=a.attname)))
    ELSE false END)
  AND NOT EXISTS(SELECT 1 FROM pg_roles untrusted WHERE has_schema_privilege(untrusted.oid,'public','CREATE')
    AND untrusted.rolname NOT IN ('kombify_user','postgres','pg_database_owner'))
  AND NOT EXISTS(SELECT 1 FROM (VALUES ('kombify_user'),('kombify_entitlements_code_owner'),('postgres')) creators(name)
    WHERE NOT EXISTS(SELECT 1 FROM pg_default_acl d WHERE d.defaclrole=creators.name::regrole AND d.defaclnamespace=0 AND d.defaclobjtype='f')
      OR EXISTS(SELECT 1 FROM pg_default_acl d,LATERAL aclexplode(d.defaclacl) a WHERE d.defaclrole=creators.name::regrole
        AND d.defaclnamespace=0 AND d.defaclobjtype='f' AND a.grantee=0 AND a.privilege_type='EXECUTE'))
FROM pg_roles r WHERE r.rolname='techstack_backup_entitlements'`).Scan(&safe)
	if err != nil || !safe {
		return nil, errors.New("backup projection effective reader authority denied")
	}
	uri, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("canonical backup projection URL denied")
	}
	query := uri.Query()
	query.Set("default_transaction_read_only", "on")
	query.Set("statement_timeout", "3000")
	query.Set("pool_max_conns", "2")
	uri.RawQuery = query.Encode()
	// DatabaseTimeout covers the whole read: a fresh TLS connection, BEGIN,
	// thirteen set_config calls, the query and COMMIT. The Stage qualification
	// reaches the database from CI through the fixed egress at about 180 ms per
	// round trip, where a 3 s budget expired (EXECUTION_QUALIFY_ENTITLEMENTS,
	// platform-j71q2). statement_timeout still bounds each statement at 3 s.
	client := featureflags.NewClient(featureflags.ClientConfig{DatabaseURL: uri.String(), Category: "kombify-techstack", ServiceName: "techstack", DatabaseTimeout: 15 * time.Second})
	if client == nil {
		return nil, errors.New("canonical backup projection client unavailable")
	}
	return client, nil
}

func checkPublicBackupCustody() error {
	var bundle map[string]string
	if raw := strings.TrimSpace(""); raw != "" {
		if json.Unmarshal([]byte(raw), &bundle) != nil {
			return errors.New("HTTP runtime requires valid scoped custody")
		}
	}
	for _, name := range []string{db.EnvBackupWorkerDatabaseURL, backupEntitlementsDSNEnv, backupWorkerModeEnv} {
		if strings.TrimSpace(os.Getenv(name)) != "" || strings.TrimSpace(bundle[name]) != "" {
			return errors.New("HTTP runtime must not receive private backup worker custody or process configuration")
		}
	}
	return nil
}
