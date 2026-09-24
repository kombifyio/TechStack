package routes

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// Before this change the managed-vs-observed distinction was recomputed at read
// time in three places with three slightly different rules. The persisted
// column is now the only answer: a route must report exactly what is stored,
// even when the provenance would suggest something else.
func TestRegistryStoreProjectionReadsThePersistedManagementState(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	stack := controlplane.Stack{ID: "stack-1", TenantID: "tenant-1", Name: "Stack"}
	node := controlplane.Node{ID: "server-1", Status: string(registryStatusRunning)}

	for _, test := range []struct {
		name, source, management, want string
	}{
		{
			name:   "observed provenance reports the stored observed value",
			source: "observed", management: "observed", want: registryObservedState,
		},
		{
			name:   "stackkits provenance reports the stored managed value",
			source: stackKitsInventorySource, management: "managed", want: registryManagedState,
		},
		{
			// An adopted service (nzy1.16) keeps the provenance that discovered
			// it. Re-deriving from `source` would silently un-adopt it.
			name:   "an adopted service keeps its stored managed value",
			source: "observed", management: "managed", want: registryManagedState,
		},
		{
			// The 074 backfill also honors legacy status/type markers, so a
			// stackkit-sourced row can legitimately be stored as observed.
			name:   "a backfilled observed row is not re-derived to managed",
			source: stackKitOutputKey, management: "observed", want: registryObservedState,
		},
		{
			// Rows written before migration 074 fail closed: never claim a
			// contract that was never declared.
			name:   "an unset value fails closed to observed",
			source: stackKitsInventorySource, management: "", want: registryObservedState,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := controlplane.Service{
				ID: "service-1", TenantID: "tenant-1", StackID: stack.ID, NodeID: node.ID,
				ServiceKey: "vaultwarden", Name: "Vaultwarden", Status: registryStatusRunning,
				Source: test.source, ManagementState: test.management,
			}
			record := serviceRegistryRecordFromStoreWithHealth(
				service, stack, node, controlplane.Worker{}, now)
			if record.ManagementState != test.want {
				t.Fatalf("management_state = %q, want %q", record.ManagementState, test.want)
			}
			// Move eligibility is the second consumer of the same axis and must
			// agree with the projection instead of deriving its own answer.
			moveAllowed, _ := registryStoreServiceMoveEligibility(service)
			if test.want == registryObservedState && moveAllowed {
				t.Fatal("an observed service was reported as movable")
			}
		})
	}
}

func TestRegistryProjectionRequiresExplicitMigrationState(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	stack := controlplane.Stack{ID: "stack-1", TenantID: "tenant-1", Name: "Stack"}
	node := controlplane.Node{ID: "server-1", Status: string(registryStatusRunning)}
	service := controlplane.Service{
		ID: "service-1", TenantID: "tenant-1", StackID: stack.ID, NodeID: node.ID,
		ServiceKey: "vaultwarden", Name: "Vaultwarden", Status: registryStatusRunning,
		ManagementState: registryObservedState,
	}

	record := serviceRegistryRecordFromStoreWithHealth(
		service, stack, node, controlplane.Worker{}, now)
	if record.MigrationStatus != "" {
		t.Fatalf("ordinary runtime status became migration_status = %q", record.MigrationStatus)
	}

	service.MigrationStatus = registryStatusMigrating
	record = serviceRegistryRecordFromStoreWithHealth(
		service, stack, node, controlplane.Worker{}, now)
	if record.MigrationStatus != registryStatusMigrating {
		t.Fatalf("explicit migration_status = %q, want %q", record.MigrationStatus, registryStatusMigrating)
	}
}

// The canonical read model must carry the dimension so the M3 UI cutover
// (kombify-Techstack-nzy1.7) cannot silently lose it.
func TestServiceRuntimeResponseCarriesTheManagementDimension(t *testing.T) {
	handlers := serviceRuntimeHandlers{now: func() time.Time { return time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC) }}
	response := handlers.response(controlplane.ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "vaultwarden", Name: "Vaultwarden", DesiredState: "running",
		ObservedState: "running", HealthState: "healthy", ManagementState: "managed",
		Source: stackKitsInventorySource,
	}, nil)
	if response.ManagementState != registryManagedState {
		t.Fatalf("canonical service response management_state = %q", response.ManagementState)
	}

	// An unset dimension fails closed rather than defaulting to managed.
	response = handlers.response(controlplane.ServiceRuntime{
		ID: "service-2", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "immich", Name: "Immich", Source: stackKitsInventorySource,
	}, nil)
	if response.ManagementState != registryObservedState {
		t.Fatalf("unset management_state = %q, want a fail-closed observed", response.ManagementState)
	}
}
