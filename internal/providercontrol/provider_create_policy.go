package providercontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// ProviderCreateReasonKillSwitchDisabled is deliberately stable and safe to
	// expose to customers. Deployment-specific details belong in operator logs,
	// not in the API error or durable request ledger.
	ProviderCreateReasonKillSwitchDisabled = "provider_create_kill_switch_disabled"
	ProviderCreateReasonSafetyIncomplete   = "provider_create_safety_incomplete"
)

var ErrProviderCreateBlocked = errors.New("providercontrol: provider create blocked")

// ProviderCreateBlockedError is the fail-closed decision for a fresh,
// cost-bearing provider create. It never applies to replay reads, polling or
// exact-handle cleanup, so an incident switch cannot strand existing custody.
type ProviderCreateBlockedError struct {
	ProviderID string
	ReasonCode string
}

func (e ProviderCreateBlockedError) Error() string {
	providerID := strings.TrimSpace(e.ProviderID)
	reason := publicProviderCreateReason(e.ReasonCode)
	if providerID == "" {
		return fmt.Sprintf("providercontrol: provider create blocked (%s)", reason)
	}
	return fmt.Sprintf("providercontrol: provider %s create blocked (%s)", providerID, reason)
}

func (ProviderCreateBlockedError) Unwrap() error { return ErrProviderCreateBlocked }

// ProviderCreateAuthorizer decides only whether a new provider create may be
// admitted. Existing intents are resolved before this seam is consulted.
type ProviderCreateAuthorizer interface {
	RequireProviderCreate(context.Context, string) error
}

// ProviderCreateDecision is immutable process configuration for one provider.
type ProviderCreateDecision struct {
	Enabled    bool
	ReasonCode string
}

// StaticProviderCreatePolicy is suitable for environment-derived boot policy.
// Missing and malformed provider identities are blocked rather than inherited
// from another provider's decision.
type StaticProviderCreatePolicy map[string]ProviderCreateDecision

func (p StaticProviderCreatePolicy) RequireProviderCreate(ctx context.Context, providerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	decision, ok := p[providerID]
	if !ok || providerID == "" {
		return ProviderCreateBlockedError{ProviderID: providerID, ReasonCode: ProviderCreateReasonKillSwitchDisabled}
	}
	if decision.Enabled {
		return nil
	}
	reason := publicProviderCreateReason(decision.ReasonCode)
	return ProviderCreateBlockedError{ProviderID: providerID, ReasonCode: reason}
}

func publicProviderCreateReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case ProviderCreateReasonKillSwitchDisabled:
		return ProviderCreateReasonKillSwitchDisabled
	case ProviderCreateReasonSafetyIncomplete:
		return ProviderCreateReasonSafetyIncomplete
	default:
		return ProviderCreateReasonKillSwitchDisabled
	}
}

// AllowProviderCreate is intentionally explicit at test and development
// composition sites; production boot uses StaticProviderCreatePolicy instead.
type AllowProviderCreate struct{}

func (AllowProviderCreate) RequireProviderCreate(ctx context.Context, _ string) error {
	return ctx.Err()
}
