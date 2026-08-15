package routes

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// InventoryAction names an authorization decision without coupling routes to
// a concrete entitlement or relational FGA backend.
type InventoryAction string

const (
	// InventoryActionRead allows secret-free inventory observation.
	InventoryActionRead InventoryAction = "read"
	// InventoryActionRILRead allows the tenant-safe RIL availability view.
	InventoryActionRILRead InventoryAction = "ril_read"
	// InventoryActionOperate allows access-context and runtime operations.
	InventoryActionOperate InventoryAction = "operate"
	// InventoryActionTransfer allows changing the responsible owner.
	InventoryActionTransfer InventoryAction = "transfer"
	// InventoryActionAdmin allows administrative inventory actions.
	InventoryActionAdmin InventoryAction = "admin"

	// InventoryEntitlementRead is the signed capability for read decisions.
	InventoryEntitlementRead = "techstack.inventory.read"
	// InventoryEntitlementRILRead is the paid RIL baseline capability. It grants
	// only the dedicated RIL summary route, never the general inventory API.
	InventoryEntitlementRILRead = "techstack.ril.read"
	// InventoryEntitlementOperate is the signed capability for operate decisions.
	InventoryEntitlementOperate = "techstack.inventory.operate"
	// InventoryEntitlementTransfer is the signed capability for transfer decisions.
	InventoryEntitlementTransfer = "techstack.inventory.transfer"
	// InventoryEntitlementAdmin is the signed capability that covers every action.
	InventoryEntitlementAdmin = "techstack.inventory.admin"
)

// ErrInventoryAccessDenied is returned for a definitive policy denial.
var ErrInventoryAccessDenied = errors.New("inventory access denied")

// InventoryAuthorization is the normalized subject, resource, and action sent
// to the injected policy for both REST and MCP calls.
type InventoryAuthorization struct {
	TenantID     string
	SubjectID    string
	ResourceType string
	ResourceID   string
	Action       InventoryAction
}

// InventoryDecision carries the immutable store scope produced by the policy.
// Application callers cannot provide or widen this scope themselves.
type InventoryDecision struct {
	ReadScope controlplane.InventoryReadScope
}

// InventoryPolicy is the shared authorization seam used by both REST and MCP.
// Hosted deployments compose signed entitlements with relational FGA; the
// owner-only implementation is reserved for self-hosted deployments.
type InventoryPolicy interface {
	AuthorizeInventory(ctx context.Context, authorization InventoryAuthorization) (InventoryDecision, error)
}

// InventoryPolicyFunc adapts a function into an InventoryPolicy.
type InventoryPolicyFunc func(context.Context, InventoryAuthorization) (InventoryDecision, error)

// AuthorizeInventory delegates the policy decision to fn.
func (fn InventoryPolicyFunc) AuthorizeInventory(ctx context.Context, authorization InventoryAuthorization) (InventoryDecision, error) {
	return fn(ctx, authorization)
}

type selfHostedInventoryPolicy struct{}

// NewSelfHostedInventoryPolicy returns the owner-scoped policy for an explicit
// self-hosted deployment. It must not be used as a hosted FGA fallback.
func NewSelfHostedInventoryPolicy() InventoryPolicy {
	return selfHostedInventoryPolicy{}
}

func (selfHostedInventoryPolicy) AuthorizeInventory(_ context.Context, authorization InventoryAuthorization) (InventoryDecision, error) {
	if strings.TrimSpace(authorization.TenantID) == "" || strings.TrimSpace(authorization.SubjectID) == "" {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	if inventoryEntitlementForAction(authorization.Action) == "" {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	readScope, err := controlplane.NewOwnerInventoryReadScope(authorization.TenantID, authorization.SubjectID)
	if err != nil || !readScope.AuthorizesTarget(authorization.ResourceType, authorization.ResourceID) {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	return InventoryDecision{ReadScope: readScope}, nil
}

func inventoryEntitlementForAction(action InventoryAction) string {
	switch action {
	case InventoryActionRead:
		return InventoryEntitlementRead
	case InventoryActionRILRead:
		return InventoryEntitlementRILRead
	case InventoryActionOperate:
		return InventoryEntitlementOperate
	case InventoryActionTransfer:
		return InventoryEntitlementTransfer
	case InventoryActionAdmin:
		return InventoryEntitlementAdmin
	default:
		return ""
	}
}

type denyInventoryPolicy struct{}

func (denyInventoryPolicy) AuthorizeInventory(context.Context, InventoryAuthorization) (InventoryDecision, error) {
	return InventoryDecision{}, ErrInventoryAccessDenied
}
