package routes

import (
	"go/ast"
	"go/parser"
	"go/token"

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

// Regression guard for the actual defect: registry.go used to hold three
// independent ownership derivations. Only the two documented single-rule
// helpers may compare against the ownership vocabulary now.
func TestRegistryRoutesHoldExactlyOneManagementDerivation(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "registry.go", nil, 0)
	if err != nil {
		t.Fatalf("parse registry.go: %v", err)
	}
	allowed := map[string]bool{
		// The two single-rule ownership reads, one per storage lane.
		"registryStoreManagementState":  true,
		"registryRecordManagementState": true,
		// Consumers of an already-resolved value, not derivations.
		"registryServiceMoveEligibility": true,
		"isRegistryManagedRecord":        true,
		// A write, not a read: the legacy import path stores the marker.
		"importUnmanagedService": true,
	}
	vocabulary := map[string]bool{"registryManagedState": true, "registryObservedState": true}
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok || decl.Body == nil || allowed[decl.Name.Name] {
			return true
		}
		ast.Inspect(decl.Body, func(inner ast.Node) bool {
			identifier, ok := inner.(*ast.Ident)
			// registryObservedState / registryManagedState are the ownership
			// vocabulary. Reading them outside the single-rule helpers means a
			// route is deciding ownership for itself again.
			if ok && vocabulary[identifier.Name] {
				t.Fatalf("%s recomputes management state at %s; read the persisted column instead",
					decl.Name.Name, fileSet.Position(identifier.Pos()))
			}
			return true
		})
		return true
	})
}
