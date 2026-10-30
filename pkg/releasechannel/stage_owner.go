package releasechannel

import (
	"context"
	"errors"
	"sync"

	"github.com/kombifyio/techstack/pkg/config"
)

var ErrStageOwnerNotAdmitted = errors.New("verified Stage owner assignment is required")
var ErrTenantNotOwned = errors.New("deployment does not own tenant execution")

// StagePolicy is installed once by the private hosted composition. The public
// source graph carries no managed identity, entitlement or database dependency.
// No HTTP field or environment flag installs a policy or grants ownership.
type StagePolicy struct {
	Prepared           func() bool
	Active             func() bool
	OwnsSubject        func(string) bool
	OwnsTenant         func(string) bool
	AdmitsJobExecution func(string) bool
	AdmitMutation      func(context.Context, string) error
	RequirePrincipal   func(context.Context) error
}

var stagePolicyState struct {
	sync.RWMutex
	policy *StagePolicy
}

// RegisterStagePolicy is a trusted composition hook, called only at startup.
// Replacing or partially installing the native policy is a programming error.
func RegisterStagePolicy(policy StagePolicy) {
	if policy.Prepared == nil || policy.Active == nil || policy.OwnsSubject == nil ||
		policy.OwnsTenant == nil || policy.AdmitsJobExecution == nil ||
		policy.AdmitMutation == nil || policy.RequirePrincipal == nil {
		panic("releasechannel: incomplete Stage policy")
	}
	stagePolicyState.Lock()
	defer stagePolicyState.Unlock()
	if stagePolicyState.policy != nil {
		panic("releasechannel: Stage policy already installed")
	}
	stagePolicyState.policy = &policy
}

func installedStagePolicy() *StagePolicy {
	stagePolicyState.RLock()
	defer stagePolicyState.RUnlock()
	return stagePolicyState.policy
}

func StageRuntimePrepared() bool {
	policy := installedStagePolicy()
	return policy != nil && policy.Prepared()
}

func StageOwnerActive() bool {
	policy := installedStagePolicy()
	return policy != nil && policy.Active()
}

func ownsStageSubject(subject string) bool {
	policy := installedStagePolicy()
	return policy != nil && policy.OwnsSubject(subject)
}

func ownsStageTenant(tenant string) bool {
	policy := installedStagePolicy()
	return policy != nil && policy.OwnsTenant(tenant)
}

func AdmitStageMutation(ctx context.Context, provider string) error {
	if policy := installedStagePolicy(); policy != nil {
		return policy.AdmitMutation(ctx, provider)
	}
	if config.ReleaseChannel() == "preview" {
		return ErrStageOwnerNotAdmitted
	}
	return nil
}

func RequireStageRequestPrincipal(ctx context.Context) error {
	if policy := installedStagePolicy(); policy != nil {
		return policy.RequirePrincipal(ctx)
	}
	if config.ReleaseChannel() == "preview" {
		return ErrStageOwnerNotAdmitted
	}
	return nil
}

// Ordinary Preview can park a request for its owning runtime. Only the private
// prepared Stage policy can admit execution for a positively bound tenant.
func AdmitsJobExecution(tenant string) bool {
	if policy := installedStagePolicy(); policy != nil {
		return policy.AdmitsJobExecution(tenant)
	}
	return true
}
