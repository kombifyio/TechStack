package activities

import (
	"context"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

// Activity payload keys — the wire contract between the ServiceMigrationWorkflow
// steps (pkg/ril/workflows/service_migration.go) and these activity
// implementations. They mirror the keys the workflow sends; keep them in sync.
const (
	keyServiceID       = "service_id"
	keyStatus          = "status"
	keyTargetServiceID = "target_service_id"
	keySourceServiceID = "source_service_id"
	keyTargetServerID  = "target_server_id"
	keyStackID         = "stack_id"
	keyFromServiceID   = "from_service_id"
	keyToServiceID     = "to_service_id"
	keyHealthy         = "healthy"
)

// ServiceRegistry is the control-plane service-record surface the migration
// activities read and write (backed by the registry services store).
type ServiceRegistry interface {
	// CreateTargetService clones the source service onto the target node in a
	// deploying state and returns the new service id.
	CreateTargetService(ctx context.Context, sourceServiceID, targetServerID, stackID string) (string, error)
	SetServiceStatus(ctx context.Context, serviceID, status string) error
	DeleteService(ctx context.Context, serviceID string) error
}

// AgentExecutor dispatches per-service workload operations to the node agent.
// Implementations are idempotency-keyed: a repeated call with the same key must
// not re-run the underlying side effect (the key maps to the agent command id).
type AgentExecutor interface {
	DeployService(ctx context.Context, serviceID, nodeID, idempotencyKey string) error
	RemoveService(ctx context.Context, serviceID, nodeID, idempotencyKey string) error
	DrainService(ctx context.Context, serviceID, idempotencyKey string) error
}

// HealthChecker reports whether a service is healthy on its current node.
type HealthChecker interface {
	ServiceHealthy(ctx context.Context, serviceID string) (bool, error)
}

// TrafficDirector repoints a service's authoritative traffic from one instance
// to another (registry pointer + reverse-proxy reconfig).
type TrafficDirector interface {
	PointTraffic(ctx context.Context, fromServiceID, toServiceID string) error
}

// Deps bundles the adapters the service-migration activities depend on. Wiring
// (cmd/techstack) provides concrete implementations over the registry store,
// the gRPC agent dispatcher, monitoring, and the traffic director.
type Deps struct {
	Registry ServiceRegistry
	Agent    AgentExecutor
	Health   HealthChecker
	Traffic  TrafficDirector
}

// RegisterServiceMigrationActivities registers the named activities the
// ServiceMigrationWorkflow dispatches onto the runner, wrapping the Deps
// adapters. Each agent-facing activity threads the step idempotency key so the
// underlying command dedups across retries/restarts.
func RegisterServiceMigrationActivities(runner *workflow.ActivityRunner, deps Deps) {
	runner.Register(workflows.ActMarkServiceStatus, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Registry.SetServiceStatus(ctx, str(in, keyServiceID), str(in, keyStatus))
	})

	runner.Register(workflows.ActCreateTargetService, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		id, err := deps.Registry.CreateTargetService(ctx, str(in, keySourceServiceID), str(in, keyTargetServerID), str(in, keyStackID))
		if err != nil {
			return nil, err
		}
		return map[string]any{keyTargetServiceID: id}, nil
	})

	runner.Register(workflows.ActDeleteService, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Registry.DeleteService(ctx, str(in, keyServiceID))
	})

	runner.Register(workflows.ActDeployServiceToNode, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Agent.DeployService(ctx, str(in, keyTargetServiceID), str(in, keyTargetServerID), workflow.IdempotencyKeyFromContext(ctx))
	})

	runner.Register(workflows.ActRemoveServiceFromNode, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Agent.RemoveService(ctx, str(in, keyTargetServiceID), str(in, keyTargetServerID), workflow.IdempotencyKeyFromContext(ctx))
	})

	runner.Register(workflows.ActDrainServiceOnNode, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Agent.DrainService(ctx, str(in, keyServiceID), workflow.IdempotencyKeyFromContext(ctx))
	})

	runner.Register(workflows.ActVerifyServiceHealthy, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		healthy, err := deps.Health.ServiceHealthy(ctx, str(in, keyTargetServiceID))
		if err != nil {
			return nil, err
		}
		return map[string]any{keyHealthy: healthy}, nil
	})

	runner.Register(workflows.ActSwitchServiceTraffic, func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return nil, deps.Traffic.PointTraffic(ctx, str(in, keyFromServiceID), str(in, keyToServiceID))
	})
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
