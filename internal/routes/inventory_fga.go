package routes

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/middleware"
)

const (
	inventoryFGARelationAccessor = "accessor"
	inventoryFGARelationCaller   = "caller"
)

var errInventoryPolicyUnavailable = errors.New("inventory policy unavailable")

// InventoryRelationshipChecker is the narrow fail-closed relation check used
// by the Inventory application service. Auth0 FGA and OpenFGA clients satisfy
// it without leaking their SDK types into route or MCP contracts.
type InventoryRelationshipChecker interface {
	Check(context.Context, string, string, string) (bool, error)
}

type fgaInventoryPolicy struct {
	checker InventoryRelationshipChecker
}

// NewInventoryFGAPolicy composes signed edge entitlements with a relational
// FGA decision. Both gates must allow the requested action. A nil checker is
// retained as a fail-closed unavailable policy so callers cannot accidentally
// fall back to owner equality in SaaS.
func NewInventoryFGAPolicy(checker InventoryRelationshipChecker) InventoryPolicy {
	return fgaInventoryPolicy{checker: checker}
}

func (p fgaInventoryPolicy) AuthorizeInventory(ctx context.Context, authorization InventoryAuthorization) (InventoryDecision, error) {
	if strings.TrimSpace(authorization.TenantID) == "" || strings.TrimSpace(authorization.SubjectID) == "" {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	relation := inventoryFGARelationForAuthorization(authorization)
	object, err := inventoryFGAObject(authorization)
	if relation == "" || err != nil {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	if !inventorySignedEntitlementAllows(ctx, authorization.Action) {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	if p.checker == nil {
		return InventoryDecision{}, errInventoryPolicyUnavailable
	}
	allowed, err := p.checker.Check(
		ctx,
		"user:"+strings.TrimSpace(authorization.SubjectID),
		relation,
		object,
	)
	if err != nil {
		return InventoryDecision{}, fmt.Errorf("%w: FGA check: %v", errInventoryPolicyUnavailable, err)
	}
	if !allowed {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	readScope, err := inventoryReadScopeForAuthorization(authorization)
	if err != nil {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	return InventoryDecision{ReadScope: readScope}, nil
}

func inventoryReadScopeForAuthorization(authorization InventoryAuthorization) (controlplane.InventoryReadScope, error) {
	tenantID := strings.TrimSpace(authorization.TenantID)
	resourceType, resourceID := strings.TrimSpace(authorization.ResourceType), strings.TrimSpace(authorization.ResourceID)
	switch resourceType {
	case controlplane.InventoryReadTargetServer, controlplane.InventoryReadTargetService:
		return controlplane.NewTenantInventoryObjectReadScope(tenantID, resourceType, resourceID)
	case controlplane.InventoryReadTargetServerCollection, controlplane.InventoryReadTargetServiceCollection, controlplane.InventoryReadTargetTools:
		// A successful collection read relation does not implicitly grant every
		// owner's rows. Regular read/operate decisions stay SQL owner-scoped;
		// only the explicit admin relation can issue a tenant collection scope.
		if authorization.Action != InventoryActionAdmin {
			return controlplane.NewOwnerInventoryReadScope(tenantID, authorization.SubjectID)
		}
		return controlplane.NewTenantInventoryCollectionReadScope(tenantID, resourceType)
	default:
		return controlplane.InventoryReadScope{}, controlplane.ErrInvalidInventoryReadScope
	}
}

func inventorySignedEntitlementAllows(ctx context.Context, action InventoryAction) bool {
	entitlements, ok := middleware.SignedEntitlementsFromContext(ctx)
	if !ok {
		return false
	}
	required := inventoryEntitlementForAction(action)
	return required != "" && (entitlements.Has(required) || entitlements.Has(InventoryEntitlementAdmin))
}

func inventoryFGARelationForAuthorization(authorization InventoryAuthorization) string {
	switch authorization.Action {
	case InventoryActionRead, InventoryActionRILRead:
		return inventoryFGARelationAccessor
	case InventoryActionOperate:
		if strings.TrimSpace(authorization.ResourceType) == controlplane.InventoryReadTargetServer &&
			strings.TrimSpace(authorization.ResourceID) != "" {
			return inventoryFGARelationCaller
		}
		return ""
	default:
		return ""
	}
}

func inventoryFGAObject(authorization InventoryAuthorization) (string, error) {
	tenantID := inventoryFGATenantSegment(authorization.TenantID)
	resourceID := strings.TrimSpace(authorization.ResourceID)
	if authorization.Action == InventoryActionRead || authorization.Action == InventoryActionRILRead {
		switch strings.TrimSpace(authorization.ResourceType) {
		case controlplane.InventoryReadTargetServer, controlplane.InventoryReadTargetServerCollection:
			return "surface:" + tenantID + "/inventory/servers", nil
		case controlplane.InventoryReadTargetService, controlplane.InventoryReadTargetServiceCollection:
			return "surface:" + tenantID + "/inventory/services", nil
		case controlplane.InventoryReadTargetTools:
			return "surface:" + tenantID + "/inventory/tools", nil
		default:
			return "", ErrInventoryAccessDenied
		}
	}
	if authorization.Action != InventoryActionOperate {
		return "", ErrInventoryAccessDenied
	}
	switch strings.TrimSpace(authorization.ResourceType) {
	case controlplane.InventoryReadTargetServer:
		if resourceID == "" {
			return "", ErrInventoryAccessDenied
		}
		return "server:" + tenantID + "/" + resourceID, nil
	default:
		return "", ErrInventoryAccessDenied
	}
}

// inventoryFGATenantSegment preserves existing organization tuple IDs while
// encoding personal tenant IDs whose colon would otherwise be parsed as a
// second FGA type separator. The encoding is deterministic and reversible,
// without leaking the raw subject into the object identifier.
func inventoryFGATenantSegment(tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if !strings.ContainsAny(tenantID, ":#") {
		return tenantID
	}
	return "b64_" + base64.RawURLEncoding.EncodeToString([]byte(tenantID))
}
