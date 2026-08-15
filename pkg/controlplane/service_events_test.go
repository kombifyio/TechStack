package controlplane

import (
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func serviceEventTestHead(desired, observed, health, status string) *serviceAggregateHead {
	return &serviceAggregateHead{
		Runtime: ServiceRuntime{
			ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
			ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
			DesiredState: desired, ObservedState: observed, HealthState: health,
			// Post-074 rows always carry a resolved ownership dimension; the
			// stackkits-inventory provenance is a rollout of ours.
			ManagementState: string(serviceregistry.ManagementManaged),
			Source:          "stackkits-inventory",
		},
		Revision: 4,
		Status:   status,
		NodeID:   "server-1",
		Exists:   true,
	}
}

func serviceEventTestCommand(patch ServiceRuntime) ServiceEvent {
	return ServiceEvent{
		TenantID: "tenant-1", ServiceID: "service-1",
		Authority: ServiceEventAuthorityGuard, Source: "stackkits-inventory",
		ObservedAt: time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC),
		Runtime:    patch,
	}
}

func TestPrepareServiceEventWritesOneTransitionPerChangedDimension(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		current    *serviceAggregateHead
		patch      ServiceRuntime
		dimensions map[string][2]string
	}{
		{
			name:    "creation records every dimension",
			current: nil,
			patch: ServiceRuntime{
				StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
				ObservedState: "running", HealthState: "healthy", Source: "stackkits-inventory",
			},
			dimensions: map[string][2]string{
				serviceDimensionDesired:    {"", "running"},
				serviceDimensionObserved:   {"", "running"},
				serviceDimensionHealth:     {"", "healthy"},
				serviceDimensionManagement: {"", "managed"},
			},
		},
		{
			name:    "health alone changes without touching observed",
			current: serviceEventTestHead("running", "running", "healthy", "running"),
			patch:   ServiceRuntime{ObservedState: "running", HealthState: "unhealthy"},
			dimensions: map[string][2]string{
				serviceDimensionHealth: {"healthy", "unhealthy"},
			},
		},
		{
			name:    "observed alone changes without touching health",
			current: serviceEventTestHead("running", "running", "unknown", "running"),
			patch:   ServiceRuntime{ObservedState: "stopped", HealthState: "unknown"},
			dimensions: map[string][2]string{
				serviceDimensionObserved: {"running", "stopped"},
			},
		},
		{
			name:       "identical observation suppresses every transition",
			current:    serviceEventTestHead("running", "running", "healthy", "running"),
			patch:      ServiceRuntime{ObservedState: "running", HealthState: "healthy"},
			dimensions: map[string][2]string{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := prepareServiceEvent(test.current, serviceEventTestCommand(test.patch), now)
			if err != nil {
				t.Fatalf("prepareServiceEvent: %v", err)
			}
			if len(prepared.transitions) != len(test.dimensions) {
				t.Fatalf("transitions = %#v, want %d", prepared.transitions, len(test.dimensions))
			}
			for _, transition := range prepared.transitions {
				want, ok := test.dimensions[transition.Dimension]
				if !ok {
					t.Fatalf("unexpected transition dimension %q", transition.Dimension)
				}
				if transition.FromState != want[0] || transition.ToState != want[1] {
					t.Fatalf("%s transition = %q -> %q, want %q -> %q",
						transition.Dimension, transition.FromState, transition.ToState, want[0], want[1])
				}
				if transition.ObservedAt != serviceEventTestCommand(test.patch).ObservedAt {
					t.Fatalf("%s transition observed_at = %v", transition.Dimension, transition.ObservedAt)
				}
			}
		})
	}
}

func TestPrepareServiceEventSuppressesIdenticalReplay(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	observedAt := now.Add(-time.Minute)
	current := serviceEventTestHead("running", "running", "healthy", "running")
	current.Runtime.ObservedAt = &observedAt
	event := serviceEventTestCommand(ServiceRuntime{
		StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
		ServiceInstance: "default", Name: "Vaultwarden", Source: "stackkits-inventory",
		ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt,
	})
	event.NodeID = "server-1"

	prepared, err := prepareServiceEvent(current, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.applied || len(prepared.transitions) != 0 || prepared.head.Revision != current.Revision {
		t.Fatalf("identical replay was applied: %#v", prepared)
	}
}

func TestPrepareServiceEventDerivesStatusFromObservedNotHealth(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("running", "starting", "starting", "starting")
	event := serviceEventTestCommand(ServiceRuntime{ObservedState: "running", HealthState: "unhealthy"})

	prepared, err := prepareServiceEvent(current, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Status != "running" {
		t.Fatalf("status = %q, want the observed projection running", prepared.head.Status)
	}
	if prepared.head.Runtime.HealthState != "unhealthy" {
		t.Fatalf("health = %q, want unhealthy", prepared.head.Runtime.HealthState)
	}
	if prepared.head.Status == prepared.head.Runtime.HealthState {
		t.Fatal("status and health must stay independent for a running-but-unhealthy service")
	}
}

func TestPrepareServiceEventRetainsControlPlaneWorkflowState(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("running", "running", "healthy", "migrating")
	current.URL = ""
	event := serviceEventTestCommand(ServiceRuntime{
		ObservedState: "running", HealthState: "healthy",
		Access: map[string]any{"mode": "direct", "url": "https://vault.example.test"},
	})
	event.URL = "https://vault.example.test"

	prepared, err := prepareServiceEvent(current, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Status != "migrating" {
		t.Fatalf("status = %q, want the retained control-plane workflow state", prepared.head.Status)
	}
	if prepared.head.URL != "" || prepared.head.Runtime.Access["mode"] != "unavailable" {
		t.Fatalf("an active migration was served as a live link: %#v", prepared.head)
	}
}

func TestPrepareServiceEventCanonicalizesAgentVocabulary(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	event := serviceEventTestCommand(ServiceRuntime{
		StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
		DesiredState: "  UP  ", ObservedState: "Reachable", HealthState: "reachable",
	})

	prepared, err := prepareServiceEvent(nil, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Runtime.DesiredState != string(serviceregistry.DesiredRunning) ||
		prepared.head.Runtime.ObservedState != string(serviceregistry.ObservedRunning) ||
		prepared.head.Runtime.HealthState != string(serviceregistry.HealthStarting) {
		t.Fatalf("agent vocabulary was not canonicalized: %#v", prepared.head.Runtime)
	}
}

func TestPrepareServiceEventKeepsUserIntentAgainstGuardObservation(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("stopped", "stopped", "unknown", "stopped")
	guard := serviceEventTestCommand(ServiceRuntime{DesiredState: "running", ObservedState: "running"})

	prepared, err := prepareServiceEvent(current, guard, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Runtime.DesiredState != "stopped" {
		t.Fatalf("Guard overwrote stored intent: %q", prepared.head.Runtime.DesiredState)
	}

	intent := serviceEventTestCommand(ServiceRuntime{DesiredState: "running"})
	intent.Authority = ServiceEventAuthorityControlPlane
	intent.Source = "owner-action"
	prepared, err = prepareServiceEvent(current, intent, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Runtime.DesiredState != "running" || len(prepared.transitions) != 1 ||
		prepared.transitions[0].Dimension != serviceDimensionDesired {
		t.Fatalf("control-plane intent was not applied: %#v", prepared)
	}
}

func TestPrepareServiceEventRejectsControlPlaneObservation(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	event := serviceEventTestCommand(ServiceRuntime{ObservedState: "running"})
	event.Authority = ServiceEventAuthorityControlPlane

	if _, err := prepareServiceEvent(serviceEventTestHead("running", "stopped", "unknown", "stopped"), event, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestPrepareServiceEventFencesExpectedRevision(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("running", "running", "healthy", "running")
	stale := int64(3)
	event := serviceEventTestCommand(ServiceRuntime{ObservedState: "stopped"})
	event.ExpectedRevision = &stale
	if _, err := prepareServiceEvent(current, event, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error = %v, want ErrConflict", err)
	}

	fresh := current.Revision
	event.ExpectedRevision = &fresh
	prepared, err := prepareServiceEvent(current, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if prepared.head.Revision != current.Revision+1 {
		t.Fatalf("revision = %d, want %d", prepared.head.Revision, current.Revision+1)
	}

	creation := int64(2)
	event.ExpectedRevision = &creation
	if _, err := prepareServiceEvent(nil, event, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("creation revision error = %v, want ErrConflict", err)
	}
}

func TestPrepareServiceEventRejectsIdentityRebinding(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("running", "running", "healthy", "running")
	event := serviceEventTestCommand(ServiceRuntime{
		StackID: "stack-2", ServiceKey: "vaultwarden", ObservedState: "running",
	})
	event.EnforceIdentityBinding = true

	if _, err := prepareServiceEvent(current, event, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}
