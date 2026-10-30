package stageowner

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

var ErrStageOwnerNotAdmitted = releasechannel.ErrStageOwnerNotAdmitted

func init() {
	releasechannel.RegisterStagePolicy(releasechannel.StagePolicy{
		Prepared: StageRuntimePrepared, Active: StageOwnerActive,
		OwnsSubject: ownsStageSubject, OwnsTenant: ownsStageTenant,
		AdmitsJobExecution: AdmitsJobExecution,
		AdmitMutation:      AdmitStageMutation, RequirePrincipal: RequireStageCohortPrincipal,
	})
}

// stageAssignment is process-local, immutable database custody. The configured
// tenant is an expected target, never authority to execute its work.
type stageAssignment struct {
	tenantID  string
	subjectID string
}

var stageOwnership struct {
	sync.RWMutex
	assignment *stageAssignment
	owner      *stageAssignment
}

// PrepareStageRuntime consumes opaque live Stage database evidence. The native
// process first verifies provider-role posture and the same-physical app pool;
// HTTP instead verifies its exact restricted Stage application role without
// opening provider custody. Neither proof nor the expected managed-lane
// assignment activates a worker; only the genuine admitted owner POST can.
func PrepareStageRuntime(binding *db.StagingRuntimeBinding, expectedTenant string) error {
	expectedTenant = strings.TrimSpace(expectedTenant)
	if config.ReleaseChannel() != "preview" || !db.DeclaresStagingDatabaseBinding() {
		return ErrStageOwnerNotAdmitted
	}
	if expectedTenant == "" || os.Getenv("TECHSTACK_DATABASE_MIGRATION_MODE") != "verify" ||
		os.Getenv("TECHSTACK_ALLOW_DB_GENESIS") != "false" || !binding.Verified() {
		return ErrStageOwnerNotAdmitted
	}
	subject := strings.TrimPrefix(expectedTenant, "usr:")
	if subject == "" || testidentity.OwnerTenant(subject) != expectedTenant ||
		!testidentity.IsTestSubject(subject) || !testidentity.IsTestTenant(expectedTenant) {
		return ErrStageOwnerNotAdmitted
	}
	stageOwnership.Lock()
	defer stageOwnership.Unlock()
	if stageOwnership.assignment != nil {
		if stageOwnership.assignment.tenantID != expectedTenant {
			return ErrStageOwnerNotAdmitted
		}
		return nil
	}
	stageOwnership.assignment = &stageAssignment{tenantID: expectedTenant, subjectID: subject}
	return nil
}

// AdmitStageMutation binds the existing worker to exactly one Stage owner only
// after the normal product mutation passed native capacity preflight. The sealed
// shared decision supplies genuine request-bound identity, grants and budget;
// an application-created FlagSet or a GET cannot activate background work.
func AdmitStageMutation(ctx context.Context, providerID string) error {
	stageOwnership.RLock()
	assignment := stageOwnership.assignment
	stageOwnership.RUnlock()
	if assignment == nil || config.ReleaseChannel() != "preview" {
		return nil
	}
	flags, ok := edgeauth.FlagsFromContext(ctx)
	if !ok {
		return monthlyruntime.ErrCapacityPolicyAuthorityUnavailable
	}
	credits, binding, err := flags.VerifiedCloudRuntimeCredits()
	if err != nil {
		return monthlyruntime.ErrCapacityPolicyAuthorityUnavailable
	}
	// The common native preflight also serves GET authority observations.
	// Only a sealed POST may bind execution; reads never activate ownership.
	if binding.Method != http.MethodPost {
		return nil
	}
	if err := RequireStageRequestPrincipal(ctx); err != nil {
		return err
	}
	id := identity.FromContext(ctx)
	if binding.SubjectID != id.UserID || binding.TenantID != id.OrgID ||
		binding.Audience != "techstack" || binding.PublicPrefix != "/v1/techstack" {
		return ErrStageOwnerNotAdmitted
	}
	if providerID != monthlyruntime.ProviderCentron && providerID != monthlyruntime.ProviderIONOS {
		return ErrStageOwnerNotAdmitted
	}
	grants, ok := middleware.SignedEntitlementsFromContext(ctx)
	if !ok {
		return ErrStageOwnerNotAdmitted
	}
	for _, feature := range monthlyruntime.RequiredFeatureKeysForProvider(providerID) {
		if !grants.Has(feature) {
			return ErrStageOwnerNotAdmitted
		}
	}
	if credits.ManagedServers.Mode == edgeauth.ManagedServerCreditModeLimited && credits.ManagedServers.Limit <= 0 {
		return &monthlyruntime.CapacityPolicyDenialError{
			TenantID: id.OrgID, OwnerSubjectID: id.UserID, ProviderID: providerID,
		}
	}
	if credits.ManagedServers.Mode != edgeauth.ManagedServerCreditModeUnlimited &&
		credits.ManagedServers.Mode != edgeauth.ManagedServerCreditModeLimited {
		return monthlyruntime.ErrCapacityPolicyAuthorityUnavailable
	}
	stageOwnership.Lock()
	defer stageOwnership.Unlock()
	if stageOwnership.owner == nil {
		stageOwnership.owner = assignment
	}
	if stageOwnership.owner.tenantID != id.OrgID || stageOwnership.owner.subjectID != id.UserID {
		return ErrStageOwnerNotAdmitted
	}
	return nil
}

// RequireStageCohortPrincipal guards request-level desired-state writes on a
// prepared Stage runtime. The Gateway routes the whole Stage test cohort
// (TECHSTACK_TEST_SUBJECTS, among them the Preview fixture owner) to this
// runtime, so each configured test identity writes in its own owner tenant and
// every other principal is refused. Execution keeps exactly one owner:
// AdmitStageMutation, AdmitsJobExecution and OwnsTenant admit only the
// assigned owner, so another member's jobs persist unclaimed on this runtime.
func RequireStageCohortPrincipal(ctx context.Context) error {
	stageOwnership.RLock()
	assignment := stageOwnership.assignment
	stageOwnership.RUnlock()
	if assignment == nil || config.ReleaseChannel() != "preview" {
		return nil
	}
	id := identity.FromContext(ctx)
	if id == nil || !id.IsAuthenticated() || !id.EmailVerified || !middleware.IsEdgeAuthenticated(ctx) {
		return ErrStageOwnerNotAdmitted
	}
	if id.UserID == assignment.subjectID && id.OrgID == assignment.tenantID {
		return nil
	}
	if _, configured := testidentity.Subjects()[id.UserID]; !configured || id.OrgID != testidentity.OwnerTenant(id.UserID) {
		return ErrStageOwnerNotAdmitted
	}
	return nil
}

// RequireStageRequestPrincipal refuses every principal but the assigned Stage
// owner before execution binds. It consumes the prepared live Stage assignment
// and the verified request identity, but never activates execution ownership.
func RequireStageRequestPrincipal(ctx context.Context) error {
	stageOwnership.RLock()
	assignment := stageOwnership.assignment
	stageOwnership.RUnlock()
	if assignment == nil || config.ReleaseChannel() != "preview" {
		return nil
	}
	id := identity.FromContext(ctx)
	if id == nil || !id.IsAuthenticated() || !id.EmailVerified || !middleware.IsEdgeAuthenticated(ctx) ||
		id.UserID != assignment.subjectID || id.OrgID != assignment.tenantID ||
		!testidentity.IsTestSubject(id.UserID) || !testidentity.IsTestTenant(id.OrgID) {
		return ErrStageOwnerNotAdmitted
	}
	return nil
}

// StageRuntimePrepared and StageOwnerActive expose only verified process state
// for transport deferral. They do not authorize a machine request; its existing
// capability/worker verifier must still prove the exact tenant, owner and lease.
func StageRuntimePrepared() bool {
	stageOwnership.RLock()
	defer stageOwnership.RUnlock()
	return config.ReleaseChannel() == "preview" && stageOwnership.assignment != nil
}

func StageOwnerActive() bool {
	stageOwnership.RLock()
	defer stageOwnership.RUnlock()
	return config.ReleaseChannel() == "preview" && stageOwnership.owner != nil
}

func ownsStageSubject(subject string) bool {
	stageOwnership.RLock()
	defer stageOwnership.RUnlock()
	return config.ReleaseChannel() == "preview" && stageOwnership.owner != nil && stageOwnership.owner.subjectID == strings.TrimSpace(subject)
}

func ownsStageTenant(tenant string) bool {
	stageOwnership.RLock()
	defer stageOwnership.RUnlock()
	return config.ReleaseChannel() == "preview" && stageOwnership.owner != nil && stageOwnership.owner.tenantID == strings.TrimSpace(tenant)
}

// AdmitsJobExecution preserves the initial request/admission handler on ordinary
// Preview: that handler parks a native operation for the owning Stable worker.
// An attested Stage runtime instead admits only its positively bound owner.
// Provider side-effect claims and background recovery still use OwnsTenant.
func AdmitsJobExecution(tenant string) bool {
	if config.ReleaseChannel() != "preview" {
		return true
	}
	stageOwnership.RLock()
	defer stageOwnership.RUnlock()
	return stageOwnership.assignment == nil ||
		(stageOwnership.owner != nil && stageOwnership.owner.tenantID == strings.TrimSpace(tenant))
}
