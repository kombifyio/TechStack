package controlplane

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultInventoryPageSize is the canonical page size when callers omit a limit.
	DefaultInventoryPageSize = 50
	// MaxInventoryPageSize bounds one authorization-scoped inventory read.
	MaxInventoryPageSize = 200
)

// ErrInvalidInventoryReadScope is returned when a caller attempts a read
// without a policy-issued tenant, owner, collection, or object scope.
var ErrInvalidInventoryReadScope = errors.New("controlplane: invalid inventory read scope")

type inventoryReadScopeMode uint8

const (
	inventoryReadScopeInvalid inventoryReadScopeMode = iota
	inventoryReadScopeOwner
	inventoryReadScopeTenantCollection
	inventoryReadScopeTenantObject
)

const (
	// InventoryReadTargetServer identifies one exact server and its dependent
	// stack/service projection reads.
	InventoryReadTargetServer = "server"
	// InventoryReadTargetService identifies one exact service.
	InventoryReadTargetService = "service"
	// InventoryReadTargetServerCollection identifies the tenant server surface.
	InventoryReadTargetServerCollection = "server_collection"
	// InventoryReadTargetServiceCollection identifies the tenant service surface.
	InventoryReadTargetServiceCollection = "service_collection"
	// InventoryReadTargetTools identifies the tenant MCP tool catalog surface.
	InventoryReadTargetTools = "inventory_tools"
)

// InventoryReadScope is an immutable, policy-issued SQL read constraint. Its
// fields are deliberately private so HTTP or MCP callers cannot substitute an
// owner or widen an exact-object decision into a tenant collection read.
type InventoryReadScope struct {
	mode           inventoryReadScopeMode
	tenantID       string
	ownerSubjectID string
	targetType     string
	targetID       string
}

// NewOwnerInventoryReadScope creates the self-hosted owner predicate.
func NewOwnerInventoryReadScope(tenantID, ownerSubjectID string) (InventoryReadScope, error) {
	tenantID, ownerSubjectID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID)
	if tenantID == "" || ownerSubjectID == "" {
		return InventoryReadScope{}, ErrInvalidInventoryReadScope
	}
	return InventoryReadScope{mode: inventoryReadScopeOwner, tenantID: tenantID, ownerSubjectID: ownerSubjectID}, nil
}

// NewTenantInventoryCollectionReadScope creates a hosted tenant-collection
// predicate after entitlement and FGA have authorized the exact surface.
func NewTenantInventoryCollectionReadScope(tenantID, targetType string) (InventoryReadScope, error) {
	tenantID, targetType = strings.TrimSpace(tenantID), strings.TrimSpace(targetType)
	switch targetType {
	case InventoryReadTargetServerCollection, InventoryReadTargetServiceCollection, InventoryReadTargetTools:
	default:
		return InventoryReadScope{}, ErrInvalidInventoryReadScope
	}
	if tenantID == "" {
		return InventoryReadScope{}, ErrInvalidInventoryReadScope
	}
	return InventoryReadScope{mode: inventoryReadScopeTenantCollection, tenantID: tenantID, targetType: targetType}, nil
}

// NewTenantInventoryObjectReadScope creates a hosted exact-object predicate
// after entitlement and FGA have authorized that object.
func NewTenantInventoryObjectReadScope(tenantID, targetType, targetID string) (InventoryReadScope, error) {
	tenantID, targetType, targetID = strings.TrimSpace(tenantID), strings.TrimSpace(targetType), strings.TrimSpace(targetID)
	if tenantID == "" || targetID == "" || (targetType != InventoryReadTargetServer && targetType != InventoryReadTargetService) {
		return InventoryReadScope{}, ErrInvalidInventoryReadScope
	}
	return InventoryReadScope{mode: inventoryReadScopeTenantObject, tenantID: tenantID, targetType: targetType, targetID: targetID}, nil
}

// TenantID returns the tenant fence carried by the policy decision.
func (s InventoryReadScope) TenantID() string { return s.tenantID }

// OwnerSubjectID returns the owner predicate, or empty for an FGA-authorized
// tenant collection/object scope.
func (s InventoryReadScope) OwnerSubjectID() string { return s.ownerSubjectID }

// IsOwnerScoped reports whether SQL must apply an owner predicate.
func (s InventoryReadScope) IsOwnerScoped() bool { return s.mode == inventoryReadScopeOwner }

// Target returns the exact object or collection bound to the decision.
func (s InventoryReadScope) Target() (string, string) { return s.targetType, s.targetID }

// AuthorizesTarget reports whether this immutable scope can back the resource
// decision requested by the application service.
func (s InventoryReadScope) AuthorizesTarget(targetType, targetID string) bool {
	targetType, targetID = strings.TrimSpace(targetType), strings.TrimSpace(targetID)
	switch s.mode {
	case inventoryReadScopeOwner:
		return s.tenantID != "" && s.ownerSubjectID != "" && inventoryReadTargetValid(targetType, targetID)
	case inventoryReadScopeTenantCollection:
		return s.tenantID != "" && s.targetType == targetType && s.targetID == "" && targetID == ""
	case inventoryReadScopeTenantObject:
		return s.tenantID != "" && s.targetType == targetType && s.targetID == targetID && targetID != ""
	default:
		return false
	}
}

// RestrictToServer returns an equal or narrower scope for a server discovered
// inside an already-authorized collection. It never widens an object scope.
func (s InventoryReadScope) RestrictToServer(serverID string) (InventoryReadScope, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return InventoryReadScope{}, ErrInvalidInventoryReadScope
	}
	switch s.mode {
	case inventoryReadScopeOwner:
		return s, nil
	case inventoryReadScopeTenantCollection:
		if s.targetType != InventoryReadTargetServerCollection && s.targetType != InventoryReadTargetServiceCollection {
			return InventoryReadScope{}, ErrInvalidInventoryReadScope
		}
		return NewTenantInventoryObjectReadScope(s.tenantID, InventoryReadTargetServer, serverID)
	case inventoryReadScopeTenantObject:
		if s.targetType == InventoryReadTargetServer && s.targetID == serverID {
			return s, nil
		}
	}
	return InventoryReadScope{}, ErrInvalidInventoryReadScope
}

func inventoryReadTargetValid(targetType, targetID string) bool {
	switch targetType {
	case InventoryReadTargetServer, InventoryReadTargetService:
		return targetID != ""
	case InventoryReadTargetServerCollection, InventoryReadTargetServiceCollection, InventoryReadTargetTools:
		return targetID == ""
	default:
		return false
	}
}

// InventoryPageKey is the stable keyset position used by the canonical,
// authorization-scoped inventory read projection. CreatedAt is immutable and ordered
// before ID so concurrent updates cannot move unread rows across a cursor.
type InventoryPageKey struct {
	CreatedAt time.Time
	ID        string
}

// IsZero reports whether the page key is incomplete and therefore unusable.
func (k InventoryPageKey) IsZero() bool {
	return k.CreatedAt.IsZero() || strings.TrimSpace(k.ID) == ""
}

// InventoryPageRequest carries a bounded keyset position and the immutable
// high watermark shared by every page in one traversal.
type InventoryPageRequest struct {
	Limit       int
	After       InventoryPageKey
	Watermark   InventoryPageKey
	FrozenEmpty bool
}

// InventoryServerPage is one policy-scoped page of server aggregates.
type InventoryServerPage struct {
	Servers   []ServerRuntime
	Watermark InventoryPageKey
	Next      *InventoryPageKey
}

// InventoryServicePage is one policy-scoped page of service projections.
type InventoryServicePage struct {
	Services  []ServiceRuntime
	Watermark InventoryPageKey
	Next      *InventoryPageKey
}

// InventoryReadStore is the canonical secret-free read seam. Implementations
// must enforce the supplied policy-issued scope in the query before returning
// rows; callers must never perform a broader read followed by Go filtering.
type InventoryReadStore interface {
	GetInventoryServer(ctx context.Context, scope InventoryReadScope, serverID string) (*ServerRuntime, error)
	GetInventoryStack(ctx context.Context, scope InventoryReadScope, stackID string) (*Stack, error)
	ListInventoryServers(ctx context.Context, scope InventoryReadScope, page InventoryPageRequest) (InventoryServerPage, error)
	ListInventoryServices(ctx context.Context, scope InventoryReadScope, serverID string, page InventoryPageRequest) (InventoryServicePage, error)
}

// InventoryServerCounter is the minimal presence read used by product
// availability gates. Implementations must apply the same policy-issued scope
// as ListInventoryServers in the database query; callers must not count a
// broader collection and filter it in process.
type InventoryServerCounter interface {
	CountInventoryServers(ctx context.Context, scope InventoryReadScope) (int64, error)
}

// CountInventoryServers returns the number of servers visible to one
// authorization-scoped principal.
func (s *MemoryStore) CountInventoryServers(_ context.Context, scope InventoryReadScope) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inventoryScopeAllowsServerCollection(scope) {
		return 0, ErrInvalidInventoryReadScope
	}
	var count int64
	for _, server := range s.servers {
		if s.inventoryServerVisibleLocked(server, scope) {
			count++
		}
	}
	return count, nil
}

func normalizeInventoryPageRequest(page InventoryPageRequest) InventoryPageRequest {
	if page.Limit <= 0 {
		page.Limit = DefaultInventoryPageSize
	}
	if page.Limit > MaxInventoryPageSize {
		page.Limit = MaxInventoryPageSize
	}
	page.After.ID = strings.TrimSpace(page.After.ID)
	page.Watermark.ID = strings.TrimSpace(page.Watermark.ID)
	page.After.CreatedAt = page.After.CreatedAt.UTC()
	page.Watermark.CreatedAt = page.Watermark.CreatedAt.UTC()
	return page
}

func inventoryPageKey(createdAt time.Time, id string) InventoryPageKey {
	return InventoryPageKey{CreatedAt: createdAt.UTC(), ID: strings.TrimSpace(id)}
}

func compareInventoryPageKeys(left, right InventoryPageKey) int {
	switch {
	case left.CreatedAt.Before(right.CreatedAt):
		return -1
	case left.CreatedAt.After(right.CreatedAt):
		return 1
	case left.ID < right.ID:
		return -1
	case left.ID > right.ID:
		return 1
	default:
		return 0
	}
}

func (s *MemoryStore) GetInventoryServer(_ context.Context, scope InventoryReadScope, serverID string) (*ServerRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	serverID = strings.TrimSpace(serverID)
	if !inventoryScopeAllowsServerRead(scope, serverID) {
		return nil, ErrInvalidInventoryReadScope
	}
	server, ok := s.servers[serverID]
	if !ok || !s.inventoryServerVisibleLocked(server, scope) {
		return nil, ErrNotFound
	}
	return cloneServerRuntime(server), nil
}

func (s *MemoryStore) GetInventoryStack(_ context.Context, scope InventoryReadScope, stackID string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stack, ok := s.stacks[strings.TrimSpace(stackID)]
	if !ok || !s.inventoryStackVisibleLocked(stack, scope) {
		return nil, ErrNotFound
	}
	return cloneStack(stack), nil
}

func (s *MemoryStore) ListInventoryServers(_ context.Context, scope InventoryReadScope, page InventoryPageRequest) (InventoryServerPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inventoryScopeAllowsServerCollection(scope) {
		return InventoryServerPage{}, ErrInvalidInventoryReadScope
	}
	page = normalizeInventoryPageRequest(page)
	if page.FrozenEmpty {
		return InventoryServerPage{}, nil
	}
	rows := make([]ServerRuntime, 0)
	for _, server := range s.servers {
		if s.inventoryServerVisibleLocked(server, scope) {
			rows = append(rows, *cloneServerRuntime(server))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return compareInventoryPageKeys(inventoryPageKey(rows[i].CreatedAt, rows[i].ID), inventoryPageKey(rows[j].CreatedAt, rows[j].ID)) < 0
	})
	watermark := page.Watermark
	if watermark.IsZero() && len(rows) > 0 {
		watermark = inventoryPageKey(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	visible := rows[:0]
	for _, row := range rows {
		key := inventoryPageKey(row.CreatedAt, row.ID)
		if (!watermark.IsZero() && compareInventoryPageKeys(key, watermark) > 0) || (!page.After.IsZero() && compareInventoryPageKeys(key, page.After) <= 0) {
			continue
		}
		visible = append(visible, row)
	}
	result := InventoryServerPage{Watermark: watermark}
	if len(visible) > page.Limit {
		visible = visible[:page.Limit]
		next := inventoryPageKey(visible[len(visible)-1].CreatedAt, visible[len(visible)-1].ID)
		result.Next = &next
	}
	result.Servers = visible
	return result, nil
}

func (s *MemoryStore) ListInventoryServices(_ context.Context, scope InventoryReadScope, serverID string, page InventoryPageRequest) (InventoryServicePage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	page = normalizeInventoryPageRequest(page)
	serverID = strings.TrimSpace(serverID)
	if !inventoryScopeAllowsServiceCollection(scope, serverID) {
		return InventoryServicePage{}, ErrInvalidInventoryReadScope
	}
	if page.FrozenEmpty {
		return InventoryServicePage{}, nil
	}
	rows, err := s.inventoryServicesLocked(scope, serverID)
	if err != nil {
		return InventoryServicePage{}, err
	}
	sort.Slice(rows, func(i, j int) bool {
		return compareInventoryPageKeys(inventoryPageKey(rows[i].CreatedAt, rows[i].ID), inventoryPageKey(rows[j].CreatedAt, rows[j].ID)) < 0
	})
	watermark := page.Watermark
	if watermark.IsZero() && len(rows) > 0 {
		watermark = inventoryPageKey(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	visible := rows[:0]
	for _, row := range rows {
		key := inventoryPageKey(row.CreatedAt, row.ID)
		if (!watermark.IsZero() && compareInventoryPageKeys(key, watermark) > 0) || (!page.After.IsZero() && compareInventoryPageKeys(key, page.After) <= 0) {
			continue
		}
		visible = append(visible, row)
	}
	result := InventoryServicePage{Watermark: watermark}
	if len(visible) > page.Limit {
		visible = visible[:page.Limit]
		next := inventoryPageKey(visible[len(visible)-1].CreatedAt, visible[len(visible)-1].ID)
		result.Next = &next
	}
	result.Services = visible
	return result, nil
}

func (s *MemoryStore) inventoryServerVisibleLocked(server ServerRuntime, scope InventoryReadScope) bool {
	if server.TenantID != scope.tenantID || !inventoryScopeAllowsServerID(scope, server.ID) {
		return false
	}
	if scope.mode == inventoryReadScopeOwner && server.OwnerSubjectID != scope.ownerSubjectID {
		return false
	}
	if strings.TrimSpace(server.StackID) == "" {
		return true
	}
	stack, ok := s.stacks[server.StackID]
	return ok && stack.TenantID == scope.tenantID && stack.OwnerSubjectID == server.OwnerSubjectID && stack.DeletedAt == nil
}

func (s *MemoryStore) inventoryStackVisibleLocked(stack Stack, scope InventoryReadScope) bool {
	if stack.TenantID != scope.tenantID || stack.DeletedAt != nil {
		return false
	}
	switch scope.mode {
	case inventoryReadScopeOwner:
		return stack.OwnerSubjectID == scope.ownerSubjectID
	case inventoryReadScopeTenantCollection:
		return scope.targetType == InventoryReadTargetServerCollection || scope.targetType == InventoryReadTargetServiceCollection
	case inventoryReadScopeTenantObject:
		if scope.targetType != InventoryReadTargetServer {
			return false
		}
		server, ok := s.servers[scope.targetID]
		return ok && server.TenantID == scope.tenantID && server.StackID == stack.ID && server.OwnerSubjectID == stack.OwnerSubjectID
	default:
		return false
	}
}

func (s *MemoryStore) inventoryServicesLocked(scope InventoryReadScope, serverID string) ([]ServiceRuntime, error) {
	rows := make([]ServiceRuntime, 0)
	projectedCounts := make(map[string]int)
	for _, service := range s.serviceRuntime {
		if service.TenantID != scope.tenantID {
			continue
		}
		visible, ok := s.visibleInventoryServiceLocked(scope, serverID, service)
		if !ok {
			continue
		}
		rows = append(rows, *visible)
		projectedCounts[service.ServerID]++
	}
	if s.inventoryServiceProjectionPendingLocked(scope, serverID, projectedCounts) {
		return nil, ErrInventoryProjectionPending
	}
	return rows, nil
}

func (s *MemoryStore) visibleInventoryServiceLocked(scope InventoryReadScope, filterServerID string, service ServiceRuntime) (*ServiceRuntime, bool) {
	server, ok := s.servers[service.ServerID]
	if !ok || !s.inventoryServerVisibleLocked(server, scope) || (filterServerID != "" && service.ServerID != filterServerID) {
		return nil, false
	}
	stack, ok := s.stacks[service.StackID]
	if !ok || stack.TenantID != scope.tenantID || stack.OwnerSubjectID != server.OwnerSubjectID || stack.DeletedAt != nil ||
		(strings.TrimSpace(server.StackID) != "" && server.StackID != service.StackID) {
		return nil, false
	}
	if server.InventoryRevision > 0 {
		revision, ok := inventoryMetadataInt64(service.Metadata, "inventory_revision")
		if !ok || revision != server.InventoryRevision {
			return nil, false
		}
	}
	return cloneServiceRuntime(service), true
}

func (s *MemoryStore) inventoryServiceProjectionPendingLocked(scope InventoryReadScope, serverID string, projectedCounts map[string]int) bool {
	for _, server := range s.servers {
		if !s.inventoryServerVisibleLocked(server, scope) || (serverID != "" && server.ID != serverID) || server.InventoryRevision <= 0 {
			continue
		}
		expected, ok := inventoryMetadataInt64(server.Metadata, "service_projection_expected")
		if !ok || expected < 0 || int64(projectedCounts[server.ID]) != expected {
			return true
		}
	}
	return false
}

func inventoryScopeAllowsServerID(scope InventoryReadScope, serverID string) bool {
	serverID = strings.TrimSpace(serverID)
	if scope.tenantID == "" || serverID == "" {
		return false
	}
	return scope.mode != inventoryReadScopeTenantObject || (scope.targetType == InventoryReadTargetServer && scope.targetID == serverID)
}

func inventoryScopeAllowsServerRead(scope InventoryReadScope, serverID string) bool {
	if !inventoryScopeAllowsServerID(scope, serverID) {
		return false
	}
	switch scope.mode {
	case inventoryReadScopeOwner:
		return scope.ownerSubjectID != ""
	case inventoryReadScopeTenantCollection:
		return scope.targetType == InventoryReadTargetServerCollection || scope.targetType == InventoryReadTargetServiceCollection
	case inventoryReadScopeTenantObject:
		return scope.targetType == InventoryReadTargetServer
	default:
		return false
	}
}

func inventoryScopeAllowsServerCollection(scope InventoryReadScope) bool {
	return (scope.mode == inventoryReadScopeOwner && scope.tenantID != "" && scope.ownerSubjectID != "") ||
		(scope.mode == inventoryReadScopeTenantCollection && scope.tenantID != "" && scope.targetType == InventoryReadTargetServerCollection)
}

func inventoryScopeAllowsServiceCollection(scope InventoryReadScope, serverID string) bool {
	switch scope.mode {
	case inventoryReadScopeOwner:
		return scope.tenantID != "" && scope.ownerSubjectID != ""
	case inventoryReadScopeTenantCollection:
		return scope.tenantID != "" && scope.targetType == InventoryReadTargetServiceCollection
	case inventoryReadScopeTenantObject:
		return scope.tenantID != "" && scope.targetType == InventoryReadTargetServer && strings.TrimSpace(serverID) == scope.targetID
	default:
		return false
	}
}

func inventoryScopeAllowsStackRead(scope InventoryReadScope) bool {
	switch scope.mode {
	case inventoryReadScopeOwner:
		return scope.tenantID != "" && scope.ownerSubjectID != ""
	case inventoryReadScopeTenantCollection:
		return scope.tenantID != "" && (scope.targetType == InventoryReadTargetServerCollection || scope.targetType == InventoryReadTargetServiceCollection)
	case inventoryReadScopeTenantObject:
		return scope.tenantID != "" && scope.targetType == InventoryReadTargetServer && scope.targetID != ""
	default:
		return false
	}
}

func inventoryScopeBoundServerID(scope InventoryReadScope) string {
	if scope.mode == inventoryReadScopeTenantObject && scope.targetType == InventoryReadTargetServer {
		return scope.targetID
	}
	return ""
}

func inventoryMetadataInt64(metadata map[string]any, key string) (int64, bool) {
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed), true
		}
	}
	return 0, false
}

var _ InventoryReadStore = (*MemoryStore)(nil)
