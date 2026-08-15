package routes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	commonedgeauth "github.com/kombifyio/go-common/edgeauth"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

type fakeInventoryRelationshipChecker struct {
	allowed  bool
	err      error
	calls    int
	user     string
	relation string
	object   string
}

type countingInventoryReadStore struct {
	controlplane.InventoryReadStore
	reads int
}

func (s *countingInventoryReadStore) GetInventoryServer(ctx context.Context, scope controlplane.InventoryReadScope, serverID string) (*controlplane.ServerRuntime, error) {
	s.reads++
	return s.InventoryReadStore.GetInventoryServer(ctx, scope, serverID)
}

func (s *countingInventoryReadStore) GetInventoryStack(ctx context.Context, scope controlplane.InventoryReadScope, stackID string) (*controlplane.Stack, error) {
	s.reads++
	return s.InventoryReadStore.GetInventoryStack(ctx, scope, stackID)
}

func (s *countingInventoryReadStore) ListInventoryServers(ctx context.Context, scope controlplane.InventoryReadScope, page controlplane.InventoryPageRequest) (controlplane.InventoryServerPage, error) {
	s.reads++
	return s.InventoryReadStore.ListInventoryServers(ctx, scope, page)
}

func (s *countingInventoryReadStore) ListInventoryServices(ctx context.Context, scope controlplane.InventoryReadScope, serverID string, page controlplane.InventoryPageRequest) (controlplane.InventoryServicePage, error) {
	s.reads++
	return s.InventoryReadStore.ListInventoryServices(ctx, scope, serverID, page)
}

func (f *fakeInventoryRelationshipChecker) Check(_ context.Context, user, relation, object string) (bool, error) {
	f.calls++
	f.user, f.relation, f.object = user, relation, object
	return f.allowed, f.err
}

func TestInventoryFGAPolicyRequiresEntitlementAndRelationship(t *testing.T) {
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	policy := NewInventoryFGAPolicy(checker)
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: "server", ResourceID: "server-1", Action: InventoryActionRead,
	}
	if _, err := policy.AuthorizeInventory(t.Context(), authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("missing entitlement error = %v, want access denied", err)
	}
	if checker.calls != 0 {
		t.Fatalf("FGA called before entitlement gate: %d", checker.calls)
	}
	featureFlagOnly := commonedgeauth.FlagsToContext(t.Context(), commonedgeauth.FlagSet{Flags: map[string]bool{InventoryEntitlementRead: true}})
	if _, err := policy.AuthorizeInventory(featureFlagOnly, authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("feature flag was treated as signed entitlement: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("FGA called for feature-flag-only request: %d", checker.calls)
	}

	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)
	decision, err := policy.AuthorizeInventory(ctx, authorization)
	if err != nil {
		t.Fatalf("authorized read: %v", err)
	}
	targetType, targetID := decision.ReadScope.Target()
	if decision.ReadScope.IsOwnerScoped() || decision.ReadScope.TenantID() != "tenant-1" || targetType != "server" || targetID != "server-1" {
		t.Fatalf("exact-object read scope = %#v", decision.ReadScope)
	}
	if checker.user != "user:owner-1" || checker.relation != inventoryFGARelationAccessor || checker.object != "surface:tenant-1/inventory/servers" {
		t.Fatalf("FGA tuple = (%q,%q,%q)", checker.user, checker.relation, checker.object)
	}

	wildcardCtx := middleware.WithSignedEntitlements(t.Context(), "*")
	if _, err := policy.AuthorizeInventory(wildcardCtx, authorization); err != nil {
		t.Fatalf("canonical signed wildcard read: %v", err)
	}
	if checker.calls != 2 {
		t.Fatalf("FGA wildcard read calls = %d, want 2 total", checker.calls)
	}

	authorization.Action = InventoryActionOperate
	if _, err := policy.AuthorizeInventory(ctx, authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("read entitlement authorized operate: %v", err)
	}
	if checker.calls != 2 {
		t.Fatalf("FGA called for entitlement denial: %d", checker.calls)
	}

	operateCtx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementOperate)
	if _, err := policy.AuthorizeInventory(operateCtx, authorization); err != nil {
		t.Fatalf("authorized operate: %v", err)
	}
	if checker.calls != 3 {
		t.Fatalf("FGA operate calls = %d, want 3 total", checker.calls)
	}
	if checker.user != "user:owner-1" || checker.relation != inventoryFGARelationCaller || checker.object != "server:tenant-1/server-1" {
		t.Fatalf("operate FGA tuple = (%q,%q,%q)", checker.user, checker.relation, checker.object)
	}
}

func TestInventoryFGAPolicyKeepsRILSummaryEntitlementRouteScoped(t *testing.T) {
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	policy := NewInventoryFGAPolicy(checker)
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: controlplane.InventoryReadTargetServerCollection,
		Action:       InventoryActionRILRead,
	}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRILRead)
	decision, err := policy.AuthorizeInventory(ctx, authorization)
	if err != nil {
		t.Fatalf("authorized RIL summary read: %v", err)
	}
	if !decision.ReadScope.IsOwnerScoped() || checker.relation != inventoryFGARelationAccessor || checker.object != "surface:tenant-1/inventory/servers" {
		t.Fatalf("RIL summary decision = scope:%#v relation:%q object:%q", decision.ReadScope, checker.relation, checker.object)
	}

	authorization.Action = InventoryActionRead
	if _, err := policy.AuthorizeInventory(ctx, authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("RIL entitlement widened the general inventory API: %v", err)
	}
}

func TestInventoryFGAObjectEncodesPersonalTenantTypeSeparators(t *testing.T) {
	authorization := InventoryAuthorization{
		TenantID: "usr:auth0|fixture", SubjectID: "auth0|fixture",
		ResourceType: controlplane.InventoryReadTargetServerCollection,
		Action:       InventoryActionRILRead,
	}
	object, err := inventoryFGAObject(authorization)
	if err != nil {
		t.Fatal(err)
	}
	if object != "surface:b64_dXNyOmF1dGgwfGZpeHR1cmU/inventory/servers" {
		t.Fatalf("personal tenant object = %q", object)
	}

	authorization.TenantID = "tenant-1"
	object, err = inventoryFGAObject(authorization)
	if err != nil || object != "surface:tenant-1/inventory/servers" {
		t.Fatalf("existing organization object changed: %q, %v", object, err)
	}
}

func TestInventoryFGAPolicyRejectsRelationsMissingFromThePinnedModel(t *testing.T) {
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	policy := NewInventoryFGAPolicy(checker)
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "admin-1",
		ResourceType: "server_collection", Action: InventoryActionAdmin,
	}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementAdmin)
	if _, err := policy.AuthorizeInventory(ctx, authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("unmodeled admin relation error = %v, want access denied", err)
	}
	if checker.calls != 0 {
		t.Fatalf("unmodeled admin relation reached FGA %d times", checker.calls)
	}
}

func TestInventoryApplicationUsesFGAExactObjectScopeForCrossOwnerRead(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-owner-2", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Delegated",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-other-tenant", TenantID: "tenant-2", OwnerSubjectID: "reader-1", Name: "Other tenant",
	}); err != nil {
		t.Fatal(err)
	}
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	app := &inventoryApplication{read: store, policy: NewInventoryFGAPolicy(checker), now: time.Now}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)

	result, err := app.serverHealth(ctx, inventoryScope{tenantID: "tenant-1", ownerID: "reader-1"}, "server-owner-2")
	if err != nil {
		t.Fatalf("cross-owner FGA read: %v", err)
	}
	if result.ServerID != "server-owner-2" {
		t.Fatalf("server health = %#v", result)
	}
	if checker.object != "surface:tenant-1/inventory/servers" || checker.relation != inventoryFGARelationAccessor {
		t.Fatalf("FGA tuple = (%q,%q)", checker.relation, checker.object)
	}

	if _, err := app.serverHealth(ctx, inventoryScope{tenantID: "tenant-1", ownerID: "reader-1"}, "server-other-tenant"); err == nil {
		t.Fatal("exact-object scope crossed the authenticated tenant")
	}
}

func TestInventoryAdminMembershipDoesNotImplicitlyWidenRESTOrMCPScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-admin-1", TenantID: "tenant-1", OwnerSubjectID: "admin-1"},
		{ID: "server-owner-2", TenantID: "tenant-1", OwnerSubjectID: "owner-2"},
		{ID: "server-other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	app := &inventoryApplication{read: store, policy: NewInventoryFGAPolicy(checker), now: time.Now}
	h := inventoryHandlers{app: app, version: "test"}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers?limit=10", "admin-1", "tenant-1", nil)
	ctx := identity.NewContext(event.Request.Context(), &identity.Identity{
		UserID: "admin-1", OrgID: "tenant-1", Roles: []string{"admin"},
	})
	event.Request = event.Request.WithContext(middleware.WithSignedEntitlements(ctx, "*"))

	if err := h.httpListServers(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin membership read status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"server-admin-1"`) || strings.Contains(body, `"server-owner-2"`) || strings.Contains(body, `"server-other-tenant"`) {
		t.Fatalf("admin membership implicitly widened inventory scope: %s", body)
	}
	if checker.calls != 1 || checker.relation != inventoryFGARelationAccessor ||
		checker.object != "surface:tenant-1/inventory/servers" {
		t.Fatalf("admin membership FGA decision = calls:%d relation:%q object:%q", checker.calls, checker.relation, checker.object)
	}

	mcpEvent, mcpRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "admin-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	mcpContext := identity.NewContext(mcpEvent.Request.Context(), &identity.Identity{
		UserID: "admin-1", OrgID: "tenant-1", Roles: []string{"admin"},
	})
	mcpEvent.Request = mcpEvent.Request.WithContext(middleware.WithSignedEntitlements(mcpContext, "*"))
	if err := h.handleMCP(mcpEvent); err != nil {
		t.Fatal(err)
	}
	if mcpRecorder.Code != http.StatusOK || !strings.Contains(mcpRecorder.Body.String(), `"tools"`) {
		t.Fatalf("admin membership MCP tools/list = %d %s", mcpRecorder.Code, mcpRecorder.Body.String())
	}
	if checker.calls != 2 || checker.relation != inventoryFGARelationAccessor ||
		checker.object != "surface:tenant-1/inventory/tools" {
		t.Fatalf("admin membership MCP FGA decision = calls:%d relation:%q object:%q", checker.calls, checker.relation, checker.object)
	}
}

func TestInventoryApplicationRegularCollectionKeepsOwnerPredicateAfterFGAAllow(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-owner-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1"},
		{ID: "server-owner-2", TenantID: "tenant-1", OwnerSubjectID: "owner-2"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	app := &inventoryApplication{read: store, policy: NewInventoryFGAPolicy(checker), now: time.Now}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)

	result, err := app.listServers(ctx, inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}, inventoryPageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Servers) != 1 || result.Servers[0].ID != "server-owner-1" {
		t.Fatalf("regular FGA collection widened owner predicate: %#v", result.Servers)
	}
}

func TestInventoryRESTAndMCPShareFGAExactObjectApplicationScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "delegated-server", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Delegated",
	}); err != nil {
		t.Fatal(err)
	}
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	h := inventoryHandlers{app: &inventoryApplication{read: store, policy: NewInventoryFGAPolicy(checker), now: time.Now}, version: "test"}

	restEvent, restRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers/delegated-server/health", "reader-1", "tenant-1", nil)
	restEvent.Request.SetPathValue("serverId", "delegated-server")
	restEvent.Request = restEvent.Request.WithContext(middleware.WithSignedEntitlements(restEvent.Request.Context(), InventoryEntitlementRead))
	if err := h.httpServerHealth(restEvent); err != nil {
		t.Fatal(err)
	}
	if restRecorder.Code != http.StatusOK || !strings.Contains(restRecorder.Body.String(), `"server_id":"delegated-server"`) {
		t.Fatalf("REST delegated read = %d %s", restRecorder.Code, restRecorder.Body.String())
	}

	mcpEvent, mcpRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "reader-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "server_health", "arguments": map[string]any{"server_id": "delegated-server"}},
	})
	mcpEvent.Request = mcpEvent.Request.WithContext(middleware.WithSignedEntitlements(mcpEvent.Request.Context(), InventoryEntitlementRead))
	if err := h.handleMCP(mcpEvent); err != nil {
		t.Fatal(err)
	}
	if mcpRecorder.Code != http.StatusOK || !strings.Contains(mcpRecorder.Body.String(), `"server_id":"delegated-server"`) || !strings.Contains(mcpRecorder.Body.String(), `"isError":false`) {
		t.Fatalf("MCP delegated read = %d %s", mcpRecorder.Code, mcpRecorder.Body.String())
	}
	if checker.calls != 2 {
		t.Fatalf("shared FGA policy calls = %d, want one per transport request", checker.calls)
	}
}

func TestInventoryApplicationFGADenialHappensBeforeStoreRead(t *testing.T) {
	read := &countingInventoryReadStore{InventoryReadStore: controlplane.NewMemoryStore()}
	checker := &fakeInventoryRelationshipChecker{allowed: false}
	app := &inventoryApplication{read: read, policy: NewInventoryFGAPolicy(checker), now: time.Now}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)

	if _, err := app.listServers(ctx, inventoryScope{tenantID: "tenant-1", ownerID: "reader-1"}, inventoryPageOptions{Limit: 10}); err == nil {
		t.Fatal("FGA denial returned no error")
	}
	if checker.calls != 1 || read.reads != 0 {
		t.Fatalf("denial calls = fga:%d store:%d, want FGA once and zero reads", checker.calls, read.reads)
	}
}

func TestInventoryApplicationRejectsMalformedPolicyDecisionBeforeStoreRead(t *testing.T) {
	otherTenant, err := controlplane.NewTenantInventoryCollectionReadScope("tenant-2", controlplane.InventoryReadTargetServerCollection)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, err := controlplane.NewOwnerInventoryReadScope("tenant-1", "owner-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		decision InventoryDecision
	}{
		{name: "zero decision"},
		{name: "wrong tenant", decision: InventoryDecision{ReadScope: otherTenant}},
		{name: "forged owner", decision: InventoryDecision{ReadScope: otherOwner}},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := &countingInventoryReadStore{InventoryReadStore: controlplane.NewMemoryStore()}
			app := &inventoryApplication{
				read: read,
				policy: InventoryPolicyFunc(func(context.Context, InventoryAuthorization) (InventoryDecision, error) {
					return test.decision, nil
				}),
				now: time.Now,
			}

			_, err := app.listServers(t.Context(), inventoryScope{tenantID: "tenant-1", ownerID: "reader-1"}, inventoryPageOptions{Limit: 10})
			var inventoryErr *inventoryError
			if !errors.As(err, &inventoryErr) || inventoryErr.status != http.StatusServiceUnavailable {
				t.Fatalf("malformed policy decision error = %#v, want fail-closed unavailable", err)
			}
			if read.reads != 0 {
				t.Fatalf("malformed decision reached store %d times", read.reads)
			}
		})
	}
}

func TestInventoryFGAPolicyFailsUnavailableWithoutCheckerOrOnError(t *testing.T) {
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: "service_collection", Action: InventoryActionRead,
	}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)
	if _, err := NewInventoryFGAPolicy(nil).AuthorizeInventory(ctx, authorization); !errors.Is(err, errInventoryPolicyUnavailable) {
		t.Fatalf("nil checker error = %v, want unavailable", err)
	}
	checker := &fakeInventoryRelationshipChecker{err: errors.New("backend unavailable")}
	if _, err := NewInventoryFGAPolicy(checker).AuthorizeInventory(ctx, authorization); !errors.Is(err, errInventoryPolicyUnavailable) {
		t.Fatalf("backend error = %v, want unavailable", err)
	}
}

func TestInventoryFGAPolicyMapsMCPToolCatalogToTenantSurface(t *testing.T) {
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	policy := NewInventoryFGAPolicy(checker)
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: "inventory_tools", Action: InventoryActionRead,
	}
	ctx := middleware.WithSignedEntitlements(t.Context(), InventoryEntitlementRead)
	if _, err := policy.AuthorizeInventory(ctx, authorization); err != nil {
		t.Fatalf("authorized tool catalog: %v", err)
	}
	if checker.relation != inventoryFGARelationAccessor || checker.object != "surface:tenant-1/inventory/tools" {
		t.Fatalf("tool catalog FGA tuple = (%q,%q)", checker.relation, checker.object)
	}
}

func TestInventoryFGAPolicyUnavailableUsesSameFailClosedRESTAndMCPApplication(t *testing.T) {
	checker := &fakeInventoryRelationshipChecker{err: errors.New("FGA backend unavailable")}
	h := inventoryHandlers{
		app: &inventoryApplication{
			read: controlplane.NewMemoryStore(), policy: NewInventoryFGAPolicy(checker), now: time.Now,
		},
		version: "test",
	}

	restEvent, restRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers", "owner-1", "tenant-1", nil)
	restEvent.Request = restEvent.Request.WithContext(middleware.WithSignedEntitlements(restEvent.Request.Context(), InventoryEntitlementRead))
	if err := h.httpListServers(restEvent); err != nil {
		t.Fatalf("REST inventory request: %v", err)
	}
	if restRecorder.Code != http.StatusServiceUnavailable || !strings.Contains(restRecorder.Body.String(), `"reason_code":"inventory_policy_unavailable"`) {
		t.Fatalf("REST response = %d %s, want fail-closed 503", restRecorder.Code, restRecorder.Body.String())
	}

	mcpEvent, mcpRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_servers", "arguments": map[string]any{}},
	})
	mcpEvent.Request = mcpEvent.Request.WithContext(middleware.WithSignedEntitlements(mcpEvent.Request.Context(), InventoryEntitlementRead))
	if err := h.handleMCP(mcpEvent); err != nil {
		t.Fatalf("MCP inventory request: %v", err)
	}
	if mcpRecorder.Code != http.StatusOK || !strings.Contains(mcpRecorder.Body.String(), `"isError":true`) ||
		!strings.Contains(mcpRecorder.Body.String(), `"status":503`) ||
		!strings.Contains(mcpRecorder.Body.String(), `"reason_code":"inventory_policy_unavailable"`) {
		t.Fatalf("MCP response = %d %s, want fail-closed tool error", mcpRecorder.Code, mcpRecorder.Body.String())
	}
}
