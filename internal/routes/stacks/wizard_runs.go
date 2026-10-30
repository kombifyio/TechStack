// POST /api/v1/wizard/runs â€” the wizard-run facade (ADR-0036 phase 3).
//
// One endpoint for both wizard run kinds: the first run founds the homelab
// with its first kit deployment, every later run expands it (found a new kit
// or join an existing deployment). The facade lives in package stacks because
// it composes the create-stack core (persist, server intent, owner bootstrap,
// dispatch) unexported here; the read-only preview sibling stays in package
// routes (internal/routes/wizard_api.go).
//
// Executable projections are validated by the pinned CLI before runtime state
// persists. Valid desired choices that a pin cannot realize are saved in the
// canonical intent and keyed ledger as blocked, without generating a plan.
// A second
// first-run against an existing homelab is structurally coerced to an
// expansion instead of minting a parallel homelab (bead kombify-Techstack-4jhr).
package stacks

import (
	"bytes"
	"context"
	"fmt"
	"github.com/kombifyio/techstack/internal/routes/auth"
	// #nosec G501 -- md5 derives the deterministic homelab id matching
	// migration 044's backfill scheme ('hl-' || md5(md5(tenant)||md5(owner)));
	// it is an identity projection, not cryptography.
	"crypto/md5" //nolint:gosec
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/internal/layoutadvice"
	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const (
	// stackConfigKeySpecV2 is the config_json key holding the projected
	// Architecture v2 stack spec â€” the join/rollout authority for native-v2
	// stacks. pkg/jobs mirrors this literal (payload contract).
	stackConfigKeySpecV2 = "stack_spec_v2"
	// stackConfigKeySpecV1State demotes the frozen user_config sibling after
	// a governed migrate-on-write. The v1 document stays available as audit
	// evidence, but stack_spec_v2 is the only join/rollout authority.
	stackConfigKeySpecV1State = "stack_spec_v1_state"
	stackConfigSpecV1Archived = "archived"

	// wizardRunFeatureKey gates the facade behind the same beta flag as the
	// preview endpoint (pkg/features/flags.go).
	wizardRunFeatureKey = "native_v2_wizard"

	maxWizardRunBodyBytes = 2 << 20

	wizardRunUserGuidanceKey = "user_guidance"
	wizardRunGuidanceTitle   = "title"
	wizardRunGuidanceBody    = "body"

	wizardRunKindFound = specv2.KitAssignmentFound
	wizardRunKindJoin  = specv2.KitAssignmentJoin

	// Queued-job literals shared with the legacy create dispatch paths in
	// crud_handlers.go.
	wizardRunProvisionJobType = "provision"
	wizardRunDeployJobType    = "deploy"
	wizardRunQueuedNoOrchStep = "Queued (no orchestrator available)"

	// Run response states: found runs enter provisioning, joins wait for the
	// new node's pairing.
	wizardRunStateField           = "state"
	wizardRunStateProvisioning    = "provisioning"
	wizardRunStateAwaitingPairing = "awaiting_pairing"
	wizardRunExistingServerIDs    = "existing_server_ids"
	wizardRunPlannedServerID      = "planned_server_id"
)

// WizardRunRouteConfig wires the wizard-run facade. Seeds, Projector,
// Validator, and Migrator are release-bound dependencies; Trust carries the
// pairing mint stores.
type WizardRunRouteConfig struct {
	AuthRuntime        auth.RuntimeStore
	Orch               *orchestrator.Orchestrator
	DeploymentMode     config.DeploymentMode
	Features           managedRuntimeFeatureChecker
	Seeds              specv2.SeedSource
	Projector          specv2.Projector
	Validator          specv2.SpecValidator
	Migrator           specv2.SpecMigrator
	ReleaseVersion     string
	Trust              trust.RouteStores
	ManagedLeases      jobs.ManagedLeaseManager
	ManagedExpansion   WizardManagedRuntimeExpansion
	ExistingServers    WizardExistingServers
	GuestProvisioner   WizardGuestProvisioner
	NotificationOutbox productnotifications.ProductEventEnqueuer
	Wallet             controlplane.WalletStore
	IdentityRuntime    WizardIdentityRuntime
	// KombifyWallet is the central kombify Wallet client. Nil means the
	// deployment reads the built-in (legacy) wallet through Wallet.
	KombifyWallet *kombifywallet.Client
	// LayoutAdvice revalidates and accepts layout recommendations a run
	// carries. Nil refuses runs that carry one.
	LayoutAdvice *layoutadvice.Service
}

// WizardManagedRuntimeExpansion is the narrow adapter boundary from the
// wizard facade to the canonical managed-runtime authority. The implementation
// lives in package routes; keeping this contract here avoids a package cycle
// without duplicating provider control, admission, or receipt logic.
type WizardManagedRuntimeExpansion interface {
	Execute(*httpx.Event, WizardManagedRuntimeExpansionRequest) (*WizardManagedRuntimeExpansionResult, error)
}

type WizardManagedRuntimeExpansionFunc func(*httpx.Event, WizardManagedRuntimeExpansionRequest) (*WizardManagedRuntimeExpansionResult, error)

func (execute WizardManagedRuntimeExpansionFunc) Execute(e *httpx.Event, request WizardManagedRuntimeExpansionRequest) (*WizardManagedRuntimeExpansionResult, error) {
	return execute(e, request)
}

type WizardManagedRuntimeExpansionRequest struct {
	StackID           string
	IdempotencyKey    string
	RuntimeSlotKey    string
	NodeRole          string
	RuntimeOfferingID string
	ProviderID        string
	ProviderRegion    string
	IONOSDatacenter   string
	StackKit          string
	Services          []string
}

// WizardManagedRuntimeExpansionResult is the secret-free correlation receipt
// copied from the canonical authority into the durable wizard-run ledger.
type WizardManagedRuntimeExpansionResult struct {
	StackID              string
	JobID                string
	RuntimeSlotKey       string
	RuntimeSlotID        string
	LeaseID              string
	RuntimeServerID      string
	ResourceGenerationID string
	OperationID          string
	ProviderID           string
	ProviderRegion       string
	IONOSDatacenter      string
	NodeRole             string
	RuntimeOfferingID    string
	EnrollmentStatus     string
	RuntimePhase         string
	IdempotentReplay     bool
	Message              string
	Warnings             []string
}

type wizardRunHandlers struct {
	crud       crudRouteHandlers
	wizardRuns controlplane.WizardRunStore
	cfg        WizardRunRouteConfig
	// identitySnapshots coalesces the dashboard's identity reads per stack.
	identitySnapshots *identitySnapshotCoalescer
}

// RegisterWizardRunRoutes registers POST /api/v1/wizard/runs. The facade is
// Postgres-first: without control-plane stores it fails closed (503), the
// PocketBase legacy lane is deliberately not supported here.
func RegisterWizardRunRoutes(r *httpx.Router, cfg WizardRunRouteConfig) {
	h := newWizardRunHandlers(cfg)
	r.POST("/api/v1/wizard/runs", h.createWizardRun)
	r.GET("/api/v1/stacks/{id}/use-cases/configuration", h.getUseCaseConfiguration)
	r.POST("/api/v1/stacks/{id}/use-cases/{workload}/configure", h.configureUseCase)
	r.POST("/api/v1/stacks/{id}/use-cases/remove", h.removeUseCase)
	// GET /api/v1/wizard/runs/active - the owner's latest run with a live job
	// snapshot; the dashboard banner and the creating page's resume use it.
	r.GET("/api/v1/wizard/runs/active", h.getActiveWizardRun)
	// POST /api/v1/wizard/runs/{runId}/dismiss - hide one run's notice for its
	// owner on every device.
	r.POST("/api/v1/wizard/runs/{runId}/dismiss", h.dismissWizardRun)
	// POST /api/v1/stacks/{id}/resume-remote-enrollment - Retry connect-remote
	// Guard enrollment on the persisted SSH connection without rerunning StackKit.
	r.POST("/api/v1/stacks/{id}/resume-remote-enrollment", h.resumeRemoteEnrollment)
	r.GET("/api/v1/stacks/{id}/identity", h.getStackIdentity)
	r.POST("/api/v1/stacks/{id}/identity/owner-activation", h.issueOwnerActivation)
	r.POST("/api/v1/stacks/{id}/identity/household-invitations", h.createHouseholdInvitation)
}

// newWizardRunHandlers builds the wizard handlers from the route config. The
// durable remote-enrollment executor uses the same construction so the queue
// handler and the HTTP routes share one wallet/pairing custody path.
func newWizardRunHandlers(cfg WizardRunRouteConfig) wizardRunHandlers {
	if !cfg.DeploymentMode.IsValid() {
		cfg.DeploymentMode = config.ModeSelfHosted
	}
	if cfg.IdentityRuntime == nil && cfg.Orch != nil {
		cfg.IdentityRuntime = orchestratorWizardIdentityRuntime{orch: cfg.Orch}
	}
	stores := currentControlPlaneStores()
	return wizardRunHandlers{
		crud: crudRouteHandlers{
			authRuntime:        cfg.AuthRuntime,
			orch:               cfg.Orch,
			deploymentMode:     cfg.DeploymentMode,
			stackStore:         stores.Stacks,
			homelabStore:       stores.Homelabs,
			jobStore:           stores.Jobs,
			walletStore:        stores.Wallet,
			activityStore:      stores.Activity,
			serverStore:        stores.Servers,
			routingStore:       stores.Routing,
			runtimeFeatures:    cfg.Features,
			managedLeases:      cfg.ManagedLeases,
			notificationOutbox: cfg.NotificationOutbox,
			kombifyWallet:      cfg.KombifyWallet,
		},
		wizardRuns:        stores.WizardRuns,
		cfg:               cfg,
		identitySnapshots: newIdentitySnapshotCoalescer(),
	}
}

// wizardRunRequest is the closed wire contract of one wizard run. Everything
// outside the WizardIntent stays in dedicated sections: owner-bootstrap
// options (first-run only), managed provider selection, and connect-remote
// SSH hints.
type wizardRunRequest struct {
	Intent specv2.WizardIntent `json:"intent"`
	// Owner passes owner-bootstrap options through to the create core
	// (owner_source, owner_email, recovery material refs). Ignored on
	// expansion runs â€” the owner already exists.
	Owner     map[string]any          `json:"owner,omitempty"`
	Managed   *wizardRunManagedParams `json:"managed,omitempty"`
	Remote    *wizardRunRemoteParams  `json:"remote,omitempty"`
	Substrate *WizardSubstrateParams  `json:"substrate,omitempty"`
	Services  []string                `json:"services,omitempty"`
}

type wizardRunManagedParams struct {
	ProviderID        string `json:"provider_id"`
	RuntimeOfferingID string `json:"runtime_offering_id,omitempty"`
	ProviderRegion    string `json:"provider_region,omitempty"`
	IONOSDatacenter   string `json:"ionos_datacenter,omitempty"`
}

type wizardRunRemoteParams struct {
	Host        string `json:"host,omitempty"`
	Port        *int   `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	AuthMethod  string `json:"auth_method,omitempty"`
	SSHKeyLabel string `json:"ssh_key_label,omitempty"`
	UseSudo     bool   `json:"use_sudo,omitempty"`
	// SSHWalletItemID names the kombify Wallet node access item that holds the
	// SSH key or password. A deployment that reads the kombify Wallet requires
	// it for both authentication methods and refuses SSHKeyLabel and Password;
	// a legacy deployment refuses it. The item id is not a secret and is kept
	// in the stack configuration as server_remote_credential_item_id.
	SSHWalletItemID string `json:"ssh_wallet_item_id,omitempty"`
	// Password is accepted for the enrollment request window and is persisted
	// in the wallet under server_remote_credential_ref; it never enters the
	// wizard ledger or stack configuration as plaintext. Legacy deployments only.
	Password string `json:"password,omitempty"`
}

// wizardRunProjectionWriteBudget bounds the response write for the persisting
// wizard run. Projection and validation run up to two pinned-CLI calls with a
// 2-minute timeout each (pkg/specv2), which exceeds the server-wide 60s write
// timeout; without this bounded extension a slow but successful run is
// disconnected after the side effects already began. The client abort budget
// (app/src/lib/api/wizardRuns.ts) must stay above this value.
const wizardRunProjectionWriteBudget = 5 * time.Minute

func (h wizardRunHandlers) createWizardRun(e *httpx.Event) error {
	_ = httpx.ExtendWriteDeadline(e.Response, wizardRunProjectionWriteBudget)
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.wizard.runs.create")
	if tenantErr != nil {
		return tenantErr
	}
	if h.crud.stackStore == nil || h.crud.homelabStore == nil || h.wizardRuns == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Wizard runs require the Postgres control plane", wizardRunDetails(
				"wizard_runs_require_control_plane", false,
				"Control plane unavailable",
				"Wizard runs persist through the Postgres control plane, which is not configured on this instance.",
			))
	}
	if !h.wizardRunEnabled(e.Request.Context(), ownerID) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"The native v2 wizard is not enabled for this account", map[string]any{
				detailsKeyReasonCode: "feature_not_enabled",
				"required_features":  []string{wizardRunFeatureKey},
				"missing_features":   []string{wizardRunFeatureKey},
				detailsKeyRetryable:  false,
				wizardRunUserGuidanceKey: map[string]any{
					wizardRunGuidanceTitle: "Beta feature required",
					wizardRunGuidanceBody:  "Enable the native_v2_wizard beta feature to run the v2 creation wizard.",
				},
			})
	}
	request, requestedIntent, intentAdjustments, ok := h.decodeWizardRun(e)
	if !ok {
		return nil
	}

	key := createStackIdempotencyKey(e)
	if len(key) > 256 {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"X-Idempotency-Key must not exceed 256 characters", wizardRunDetails(
				"wizard_idempotency_key_invalid", false,
				"Idempotency-Key too long",
				"Use an Idempotency-Key of at most 256 characters.",
			))
	}

	if wizardRunTransport(request) == specv2.TransportHypervisor && key == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Hypervisor provisioning requires an Idempotency-Key", nil)
	}
	run := wizardRunState{
		runID:             uuid.NewString(),
		tenantID:          tenantID,
		ownerID:           ownerID,
		request:           request,
		explicitIntent:    request.Intent,
		requestedIntent:   requestedIntent,
		intentAdjustments: intentAdjustments,
		requestHash:       wizardRunRequestHash(wizardRunRequestWithIntent(request, requestedIntent)),
		key:               key,
	}
	if key != "" {
		// Bind the run identity to the existing tenant/owner/attempt key even
		// when an interrupted first delivery cannot write its failure ledger.
		run.runID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("wizard-run:"+deterministicStackID(tenantID, ownerID, key))).String()
	}
	if handled := h.replayWizardRunFromLedger(e, &run); handled {
		return nil
	}
	if handled := h.prepareWizardRun(e, &run); handled {
		return nil
	}
	if handled := h.reserveWizardRun(e, &run); handled {
		return nil
	}
	if len(run.blockedCapabilities) > 0 {
		return h.persistBlockedWizardRun(e, &run)
	}
	switch request.Intent.KitAssignment.Mode {
	case wizardRunKindJoin:
		return h.runWizardJoin(e, &run)
	case wizardRunKindExtend:
		return h.runWizardExtend(e, &run)
	default:
		return h.runWizardFound(e, &run)
	}
}

// getActiveWizardRun serves the owner's most recent wizard run together with
// a live snapshot of its provision job and the server it targets. A run whose
// notice is over (dismissed, terminal, silent past wizardRunNoticeStaleAfter or
// superseded by a later completed rollout) reads as {run: null}, exactly like
// no run at all; see wizardRunNoticeCurrent.
func (h wizardRunHandlers) getActiveWizardRun(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.wizard.runs.read")
	if tenantErr != nil {
		return tenantErr
	}
	if h.crud.stackStore == nil || h.wizardRuns == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Wizard runs require the Postgres control plane", wizardRunDetails(
				"wizard_runs_require_control_plane", false,
				"Control plane unavailable",
				"Wizard runs persist through the Postgres control plane, which is not configured on this instance.",
			))
	}
	if !h.wizardRunEnabled(e.Request.Context(), ownerID) {
		return httpx.Success(e, http.StatusOK, map[string]any{"run": nil})
	}
	run, err := h.wizardRuns.GetLatestWizardRunByOwner(e.Request.Context(), tenantID, ownerID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.Success(e, http.StatusOK, map[string]any{"run": nil})
		}
		httpx.RecordDependencyFailure(e, "wizard_runs.active.latest", err)
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to read the wizard-run ledger", nil)
	}
	job, groupErr := h.wizardRunOperationJob(e.Request.Context(), tenantID, run)
	if groupErr != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard operation outcomes are unavailable", nil)
	}
	if !h.wizardRunNoticeCurrent(e.Request.Context(), tenantID, run, job, time.Now()) {
		return httpx.Success(e, http.StatusOK, map[string]any{"run": nil})
	}
	payload := map[string]any{
		"run_id":             run.ID,
		routingStatusKey:     run.Status,
		"run_kind":           run.RunKind,
		"requested_run_kind": run.RequestedRunKind,
		"homelab_id":         run.HomelabID,
		creationStackIDField: run.StackID,
		"node_id":            run.NodeID,
		creationJobIDField:   run.JobID,
		"pairing_job_id":     run.PairingJobID,
		"error_reason":       run.ErrorReason,
		"created_at":         run.CreatedAt,
		"updated_at":         run.UpdatedAt,
		"result":             run.Result,
	}
	payload["job"] = wizardRunJobSnapshot(job)
	payload["target_server_id"] = wizardRunTargetServerID(run, job)
	return httpx.Success(e, http.StatusOK, map[string]any{"run": payload})
}

// wizardRunSupersessionHops bounds how far a run follows recovery
// replacements of its creation job.
const wizardRunSupersessionHops = 4

// wizardRunJob reads the run's provision job (nil when the run has no job or
// the job store cannot serve it). The ledger keeps the job the run dispatched;
// when recovery handed that job over to a deterministic replacement, the
// replacement is the live job behind the run.
func (h wizardRunHandlers) wizardRunJob(ctx context.Context, tenantID, jobID string) *controlplane.Job {
	if h.crud.jobStore == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	job, err := h.crud.jobStore.GetJob(ctx, tenantID, jobID)
	if err != nil {
		return nil
	}
	for hop := 0; hop < wizardRunSupersessionHops; hop++ {
		replacementID := jobs.SupersededByJobID(job.ID, job.Result)
		if replacementID == "" {
			break
		}
		replacement, replacementErr := h.crud.jobStore.GetJob(ctx, tenantID, replacementID)
		if replacementErr != nil || replacement.StackID != job.StackID {
			break
		}
		job = replacement
	}
	return job
}

// wizardRunJobSnapshot projects the provision job's live state for the notice.
func wizardRunJobSnapshot(job *controlplane.Job) map[string]any {
	if job == nil {
		return nil
	}
	return map[string]any{
		"id":                 job.ID,
		wizardRunStateField:  job.State,
		"progress":           job.Progress,
		"step":               job.Step,
		creationMessageField: job.Message,
		"type":               job.Type,
		"updated_at":         job.UpdatedAt,
	}
}

// wizardRunState threads the per-run facts through the flow helpers.
type wizardRunState struct {
	blockedCapabilities []wizardBlockedCapability
	runID               string
	tenantID            string
	ownerID             string
	request             wizardRunRequest
	// Only these normalized incoming decisions may be written back. Inherited
	// values are for admission/projection, never a stale replacement delta.
	explicitIntent    specv2.WizardIntent
	requestedIntent   specv2.WizardIntent
	intentAdjustments []specv2.IntentAdjustment
	requestHash       string
	key               string
	// homelabID is deterministic and computed up front; the homelab ROW is
	// created lazily (ensureWizardHomelab) only once the run passes
	// validation, so a rejected run leaves no state behind.
	homelabID string
	homelab   *controlplane.Homelab
	// priorUnmappedGoals is the durable backlog from the owner's homelab.
	// activatedGoals is the subset the pinned release projected successfully
	// during this run, used for one idempotent owner notification per goal.
	priorUnmappedGoals []string
	activatedGoals     []string
	// resumeStack is the deterministic keyed stack when this request is a
	// same-key retry of a run that already persisted its stack row; resume
	// runs skip coercion and free-name resolution so they converge on the
	// original outcome instead of founding divergent state.
	resumeStack *controlplane.Stack
	// priorFailed is the ledger entry of an earlier failed attempt with this
	// key; the join lane uses its NodeID to avoid appending a second node.
	priorFailed  *controlplane.WizardRun
	pairingJobID string
	// evaluatedGoals is the original projection's scope. Nil is unknown legacy
	// scope; an empty slice records that no backlog goal was evaluated.
	evaluatedGoals []string
	// existingServerIDs is the canonical pre-join runtime baseline. Keeping it
	// in the durable result lets another tab or device distinguish the newly
	// paired server from an already healthy server on the same deployment.
	existingServerIDs []string
	// effectiveKind is the run kind after coercion; requested kind stays in
	// request.Intent.RunKind.
	effectiveKind string
	// layoutRecommendationID is the accepted layout recommendation this run
	// carries out, set only after revalidation accepted it.
	layoutRecommendationID string
}

func (run *wizardRunState) coerced() bool {
	return run.effectiveKind != run.request.Intent.RunKind
}

// decodeWizardRun reads and validates the closed request contract. It returns
// ok == false after writing the error response.
func (h wizardRunHandlers) decodeWizardRun(e *httpx.Event) (wizardRunRequest, specv2.WizardIntent, []specv2.IntentAdjustment, bool) {
	var request wizardRunRequest
	body, readErr := io.ReadAll(io.LimitReader(e.Request.Body, maxWizardRunBodyBytes+1))
	if readErr != nil || len(body) == 0 || len(body) > maxWizardRunBodyBytes {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Wizard run request body is missing, unreadable, or too large", wizardRunDetails(
				"wizard_request_invalid", false,
				"Request not understood",
				"Send the wizard run as a JSON body of at most 2 MiB.",
			))
		return request, specv2.WizardIntent{}, nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Invalid wizard run request: "+decodeErr.Error(), wizardRunDetails(
				"wizard_request_invalid", false,
				"Request not understood",
				"The wizard run payload does not match the closed request contract.",
			))
		return request, specv2.WizardIntent{}, nil, false
	}
	requestedIntent := request.Intent
	normalizedIntent, intentAdjustments := specv2.NormalizeCreationIntent(request.Intent)
	request.Intent = normalizedIntent
	if validateErr := request.Intent.Validate(); validateErr != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Invalid wizard intent: "+validateErr.Error(), wizardRunDetails(
				"wizard_intent_invalid", false,
				"Intent rejected",
				"The wizard intent failed the closed contract validation.",
			))
		return request, requestedIntent, intentAdjustments, false
	}
	if !h.validateWizardGuestRequest(e, request) {
		return request, requestedIntent, intentAdjustments, false
	}
	if denial := h.remoteCredentialSourceDenial(request); denial != nil {
		_ = denial.Write(e)
		return request, requestedIntent, intentAdjustments, false
	}
	return request, requestedIntent, intentAdjustments, true
}

func wizardRunRequestWithIntent(request wizardRunRequest, intent specv2.WizardIntent) wizardRunRequest {
	request.Intent = intent
	return request
}

func (h wizardRunHandlers) wizardRunEnabled(ctx context.Context, userID string) bool {
	if h.crud.runtimeFeatures == nil {
		return false
	}
	enabled, err := h.crud.runtimeFeatures.IsEnabled(ctx, wizardRunFeatureKey, userID)
	return err == nil && enabled
}

// replayWizardRunFromLedger serves a completed ledger entry for the request's
// Idempotency-Key. Failed entries never block a retry (they are kept on the
// run state so the lanes can converge on the earlier partial outcome); a
// completed entry with a different request fingerprint is a key-reuse
// conflict.
func (h wizardRunHandlers) replayWizardRunFromLedger(e *httpx.Event, run *wizardRunState) bool {
	if run.key == "" {
		return false
	}
	stored, err := h.wizardRuns.GetWizardRunByKey(e.Request.Context(), run.tenantID, run.ownerID, run.key)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return false
		}
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Wizard run ledger unavailable", wizardRunDetails(
				"wizard_ledger_unavailable", true,
				"Try again",
				"The wizard-run ledger could not be read; retry with the same Idempotency-Key.",
			))
		return true
	}
	if stored.Status != "completed" {
		if (wizardRunTransport(run.request) == specv2.TransportHypervisor || stored.ErrorReason == "projection_pending" || stored.ErrorReason == "intent_projection_failed" || stored.ErrorReason == controlplane.WizardIntentProjectionPending || stored.ErrorReason == controlplane.WizardDesiredProjectionBlocked) && stored.RequestSHA256 != run.requestHash {
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key already identifies a different projection request", nil)
			return true
		}
		run.runID = stored.ID
		run.pairingJobID = stored.PairingJobID
		run.priorFailed = stored
		return false
	}
	if stored.RequestSHA256 != run.requestHash {
		_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Idempotency-Key was already used for a different wizard run", wizardRunConflictDetails(stored))
		return true
	}
	response := map[string]any{}
	for key, value := range stored.Result {
		response[key] = value
	}
	response["run_id"] = stored.ID
	response["idempotent_replay"] = true
	_ = httpx.Success(e, http.StatusAccepted, response)
	return true
}

// Every keyed lane crosses this same ledger boundary before mutation. A late
// writer cannot bypass a blocked reservation just because it has no Stack row.
func (h wizardRunHandlers) reserveWizardRun(e *httpx.Event, run *wizardRunState) bool {
	if run.key == "" {
		return false
	}
	entry := h.wizardRunLedgerEntry(run, controlplane.WizardRun{
		Status: "failed", ErrorReason: controlplane.WizardIntentProjectionPending,
	}, map[string]any{})
	stored, err := h.wizardRuns.ReserveWizardRun(e.Request.Context(), entry)
	if errors.Is(err, controlplane.ErrConflict) {
		_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Idempotency-Key already identifies a different projection request", wizardRunConflictDetails(nil))
		return true
	}
	if err != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Wizard run reservation unavailable; retry with the same Idempotency-Key", nil)
		return true
	}
	if stored.Status == "completed" {
		return h.replayWizardRunFromLedger(e, run)
	}
	run.runID, run.pairingJobID, run.priorFailed = stored.ID, stored.PairingJobID, stored
	if stored.StackID != "" && run.resumeStack == nil {
		stack, err := h.crud.stackStore.GetStack(e.Request.Context(), run.tenantID, stored.StackID)
		if err != nil || stack.OwnerSubjectID != run.ownerID {
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Reserved Wizard deployment unavailable; retry with the same Idempotency-Key", nil)
			return true
		}
		if run.request.Intent.KitAssignment.Mode == wizardRunKindJoin {
			// A join's target predates this attempt. It is not a created Stack
			// to resume; actual Node/job/pairing refs remain recovery guards.
			if stack.ID != run.request.Intent.KitAssignment.KitDeploymentID {
				_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Reserved Wizard target differs from the requested join target", wizardRunConflictDetails(stored))
				return true
			}
		} else {
			run.resumeStack = stack
		}
	}
	return false
}

// prepareWizardRun computes the deterministic homelab id, detects a same-key
// resume, and decides the effective run kind: a first-run against a homelab
// that already operates kit deployments is coerced to an expansion (never a
// second homelab, never a parallel first-run). A resume is exempt from
// coercion â€” the first delivery already fixed the run's shape, and re-coercing
// would strip the owner section and diverge the retry from the original.
func (h wizardRunHandlers) prepareWizardRun(e *httpx.Event, run *wizardRunState) bool {
	ctx := e.Request.Context()
	run.homelabID = deterministicHomelabID(run.tenantID, run.ownerID)

	if run.key != "" && run.request.Intent.KitAssignment.Mode != wizardRunKindJoin && run.request.Intent.KitAssignment.Mode != wizardRunKindExtend {
		keyed, err := h.crud.stackStore.GetStack(ctx, run.tenantID, deterministicStackID(run.tenantID, run.ownerID, run.key))
		if err == nil && keyed.OwnerSubjectID == run.ownerID {
			run.resumeStack = keyed
		} else if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Wizard run resume lookup unavailable", wizardRunDetails(
					"wizard_ledger_unavailable", true,
					"Try again",
					"The keyed stack lookup failed; retry with the same Idempotency-Key.",
				))
			return true
		}
	}

	run.effectiveKind = run.request.Intent.RunKind
	if run.request.Intent.KitAssignment.Mode == wizardRunKindJoin || run.request.Intent.KitAssignment.Mode == wizardRunKindExtend {
		// Joining or extending an existing deployment is by definition an expansion.
		run.effectiveKind = specv2.RunKindExpansion
	}
	if run.effectiveKind == specv2.RunKindFirstRun && run.resumeStack == nil {
		count, countErr := h.activeOwnedStackCount(ctx, run.tenantID, run.ownerID)
		if countErr != nil {
			_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to inspect existing kit deployments", nil)
			return true
		}
		if count > 0 {
			run.effectiveKind = specv2.RunKindExpansion
		}
	}
	if run.effectiveKind == specv2.RunKindExpansion {
		// The owner already exists; expansion runs never re-bootstrap it.
		run.request.Owner = nil
	}
	// A saved blocked first run may have no deployment yet. Reconcile it too;
	// admission must always see inherited decisions, not only inherited names.
	if err := h.loadStoredWizardGoals(ctx, run); err != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stored Wizard goals could not be reconciled", wizardRunDetails(
				"wizard_goal_reconciliation_unavailable", true,
				"Try again",
				"The owner's stored Wizard goals could not be read; no projection was attempted.",
			))
		return true
	}
	if err := validateUseCaseSettingsAgainstCatalog(run.request.Intent); err != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Wizard use-case settings rejected", wizardRunDetails(
				"wizard_use_case_setting_rejected", false, "Change the setting", err.Error()))
		return true
	}
	effectiveIntent, blocked, admissionErr := admitWizardDesiredCapabilities(run.request.Intent)
	if admissionErr != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Wizard use-case capabilities rejected", wizardRunDetails(
				"wizard_use_case_capability_rejected", false, "Change the capability", admissionErr.Error()))
		return true
	}
	run.request.Intent = effectiveIntent
	run.blockedCapabilities = blocked
	if !admitWizardGuestAccelerator(e, &run.request) {
		return true
	}
	return false
}

// validateUseCaseSettingsAgainstCatalog closes the settings contract: every
// use case must be one the resolved catalog knows, every setting one its
// configuration projection declares, and every value one the declaration
// accepts. The resolver is shared with the catalog API, so a client can submit
// exactly the choices that API advertised from either the image catalog or the
// embedded pin.
func validateUseCaseSettingsAgainstCatalog(intent specv2.WizardIntent) error {
	if len(intent.UseCaseSettings) == 0 {
		return nil
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return fmt.Errorf("the StackKits use-case catalog could not be read: %w", err)
	}
	for slug, values := range intent.UseCaseSettings {
		useCase, ok := catalog.FindUseCase(slug)
		if !ok {
			return fmt.Errorf("use case %q is not in the catalog", slug)
		}
		for id, value := range values {
			setting, ok := useCase.FindSetting(id)
			if !ok {
				return fmt.Errorf("use case %q declares no setting %q", slug, id)
			}
			if !setting.AcceptsSettingValue(value) {
				return fmt.Errorf("use case %q setting %q does not accept the given value", slug, id)
			}
		}
	}
	return nil
}

// validateUseCaseCapabilitiesAgainstCatalog closes the capability contract:
// each capability must be declared by the use case, and its value must be the
// default option, the alternative option or "off" (never for a required
// capability). A recorded option is rejected because the release cannot
// install it.
func validateUseCaseCapabilitiesAgainstCatalog(intent specv2.WizardIntent) error {
	if len(intent.UseCaseCapabilities) == 0 {
		return nil
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return fmt.Errorf("the StackKits use-case catalog could not be read: %w", err)
	}
	for slug, values := range intent.UseCaseCapabilities {
		useCase, ok := catalog.FindUseCase(slug)
		if !ok {
			return fmt.Errorf("use case %q is not in the catalog", slug)
		}
		for id, value := range values {
			capability, ok := useCase.FindCapability(id)
			if !ok {
				return fmt.Errorf("use case %q declares no capability %q", slug, id)
			}
			if err := capability.AcceptsSelection(value); err != nil {
				return fmt.Errorf("use case %q: %w", slug, err)
			}
		}
	}
	return nil
}

// ensureWizardHomelab founds (or loads) the owner's homelab row. Called only
// after the run passed projection + CLI validation so a rejected run creates
// nothing; the id is deterministic, so validation already used the same value
// as fleetRef.
func (h wizardRunHandlers) ensureWizardHomelab(e *httpx.Event, run *wizardRunState, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultCreateStackName
	}
	homelab, err := h.crud.homelabStore.GetOrCreateHomelabForOwner(e.Request.Context(), controlplane.CreateHomelabRequest{
		ID:             run.homelabID,
		TenantID:       run.tenantID,
		OwnerSubjectID: run.ownerID,
		Name:           name,
	})
	if err != nil {
		_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to resolve homelab", nil)
		return true
	}
	run.homelab = homelab
	return false
}

func (h wizardRunHandlers) activeOwnedStackCount(ctx context.Context, tenantID, ownerID string) (int, error) {
	stacks, err := h.crud.stackStore.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, stack := range stacks {
		if stack.OwnerSubjectID == ownerID {
			count++
		}
	}
	return count, nil
}

// deterministicHomelabID mirrors migration 044's backfill scheme so a wizard
// first-run racing the backfill (or a crash-retry) converges on one id:
// 'hl-' || md5(md5(tenant) || md5(owner)).
func deterministicHomelabID(tenantID, ownerID string) string {
	inner := md5Hex(tenantID) + md5Hex(ownerID)
	return "hl-" + md5Hex(inner)
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value)) // #nosec G401 -- identity derivation, see package comment on the import.
	return hex.EncodeToString(sum[:])
}

// wizardRunRequestHash fingerprints the run request for the idempotency
// ledger; a replayed key must carry the identical payload.
func wizardRunRequestHash(request wizardRunRequest) string {
	payload, err := json.Marshal(request)
	if err != nil {
		payload = []byte(request.Intent.Name + "\x00" + request.Intent.RunKind)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// wizardRunDetails builds the structured denial envelope shared with the
// create-stack denial surfaces (reason_code, retryable, user_guidance).
func wizardRunDetails(reasonCode string, retryable bool, title, body string) map[string]any {
	return map[string]any{
		detailsKeyReasonCode: reasonCode,
		detailsKeyRetryable:  retryable,
		wizardRunUserGuidanceKey: map[string]any{
			wizardRunGuidanceTitle: title,
			wizardRunGuidanceBody:  body,
		},
	}
}

func wizardRunConflictDetails(stored *controlplane.WizardRun) map[string]any {
	details := wizardRunDetails(
		"wizard_idempotency_conflict",
		true,
		"Resume the completed attempt or start fresh",
		"This browser session reused an attempt key from a different submission. Resume the earlier registration or start a fresh attempt with a new key.",
	)
	if stored == nil {
		return details
	}
	details["completed_run_id"] = stored.ID
	if stored.StackID != "" {
		details[creationStackIDField] = stored.StackID
	}
	if stored.NodeID != "" {
		details["node_id"] = stored.NodeID
	}
	if stored.JobID != "" {
		details[creationJobIDField] = stored.JobID
	}
	if stored.PairingJobID != "" {
		details["pairing_job_id"] = stored.PairingJobID
	}
	return details
}
