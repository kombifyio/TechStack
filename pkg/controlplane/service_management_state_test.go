package controlplane

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func managementTestHead(source, management string) *serviceAggregateHead {
	head := serviceEventTestHead("running", "running", "healthy", "running")
	head.Runtime.Source = source
	head.Runtime.ManagementState = management
	return head
}

// The management dimension follows exactly the same transition contract as
// desired/observed/health: one row per dimension that actually changed, nothing
// for a no-op, and always under the compare-and-swap fence.
func TestPrepareServiceEventWritesManagementTransitionsChangeOnly(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name           string
		current        *serviceAggregateHead
		authority      string
		patch          ServiceRuntime
		wantManagement string
		wantTransition [2]string // empty pair means "no management transition"
	}{
		{
			name:    "creation from an observed provenance records the first ownership",
			current: nil,
			patch: ServiceRuntime{
				StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
				Source: "observed", ObservedState: "running", HealthState: "healthy",
			},
			wantManagement: "observed",
			wantTransition: [2]string{"", "observed"},
		},
		{
			name:    "creation from a stackkits rollout records managed",
			current: nil,
			patch: ServiceRuntime{
				StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
				Source: "stackkits-inventory", ObservedState: "running", HealthState: "healthy",
			},
			wantManagement: "managed",
			wantTransition: [2]string{"", "managed"},
		},
		{
			name:           "an unchanged provenance writes no management row",
			current:        managementTestHead("stackkits-inventory", "managed"),
			patch:          ServiceRuntime{ObservedState: "running", HealthState: "healthy"},
			wantManagement: "managed",
		},
		{
			name:           "a provenance change re-derives ownership and records it",
			current:        managementTestHead("observed", "observed"),
			patch:          ServiceRuntime{Source: "stackkits-inventory", ObservedState: "running", HealthState: "healthy"},
			wantManagement: "managed",
			wantTransition: [2]string{"observed", "managed"},
		},
		{
			// The 074 backfill also honors the legacy status/type markers the
			// source column cannot express, so a stored value must survive an
			// ordinary observation of the same provenance.
			name:           "a backfilled ownership is not re-derived away",
			current:        managementTestHead("stackkit_outputs", "observed"),
			patch:          ServiceRuntime{Source: "stackkit_outputs", ObservedState: "running", HealthState: "healthy"},
			wantManagement: "observed",
		},
		{
			name:           "an explicit control-plane assertion wins",
			current:        managementTestHead("observed", "observed"),
			authority:      ServiceEventAuthorityControlPlane,
			patch:          ServiceRuntime{ManagementState: "managed"},
			wantManagement: "managed",
			wantTransition: [2]string{"observed", "managed"},
		},
		{
			// Guard measures runtime, it does not declare ownership.
			name:           "a guard ownership claim is ignored",
			current:        managementTestHead("observed", "observed"),
			patch:          ServiceRuntime{ManagementState: "managed", ObservedState: "running", HealthState: "healthy"},
			wantManagement: "observed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := serviceEventTestCommand(test.patch)
			if test.authority != "" {
				event.Authority = test.authority
				event.Source = "owner-action"
			}
			prepared, err := prepareServiceEvent(test.current, event, now)
			if err != nil {
				t.Fatalf("prepareServiceEvent: %v", err)
			}
			if prepared.head.Runtime.ManagementState != test.wantManagement {
				t.Fatalf("management_state = %q, want %q",
					prepared.head.Runtime.ManagementState, test.wantManagement)
			}
			var got [2]string
			found := 0
			for _, transition := range prepared.transitions {
				if transition.Dimension != serviceDimensionManagement {
					continue
				}
				found++
				got = [2]string{transition.FromState, transition.ToState}
			}
			if test.wantTransition == [2]string{} {
				if found != 0 {
					t.Fatalf("a no-op wrote %d management transitions: %#v", found, prepared.transitions)
				}
				return
			}
			if found != 1 || got != test.wantTransition {
				t.Fatalf("management transitions = %d %v, want one %v", found, got, test.wantTransition)
			}
		})
	}
}

// The management dimension must not weaken the compare-and-swap fence: a stale
// expected revision still loses even when ownership would change.
func TestPrepareServiceEventFencesManagementBehindTheExpectedRevision(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := managementTestHead("observed", "observed")
	stale := current.Revision - 1
	event := serviceEventTestCommand(ServiceRuntime{ManagementState: "managed"})
	event.Authority = ServiceEventAuthorityControlPlane
	event.Source = "owner-action"
	event.ExpectedRevision = &stale

	if _, err := prepareServiceEvent(current, event, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}

	fresh := current.Revision
	event.ExpectedRevision = &fresh
	prepared, err := prepareServiceEvent(current, event, now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	if !prepared.applied || prepared.head.Revision != current.Revision+1 {
		t.Fatalf("management change did not advance the fence: %#v", prepared)
	}
}

// A byte-identical replay of an ownership-carrying command must stay a no-op.
func TestPrepareServiceEventSuppressesIdenticalManagementReplay(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	observedAt := now.Add(-time.Minute)
	current := managementTestHead("stackkits-inventory", "managed")
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

// An observed service has no declared contract, so a declared target state is
// refused with a reason code instead of being invented.
func TestPrepareServiceEventRejectsDesiredStateForObservedServices(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	current := managementTestHead("observed", "observed")
	event := serviceEventTestCommand(ServiceRuntime{DesiredState: "stopped"})
	event.Authority = ServiceEventAuthorityControlPlane
	event.Source = "owner-action"

	_, err := prepareServiceEvent(current, event, now)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), serviceregistry.ReasonDesiredStateNotApplicable) {
		t.Fatalf("error = %v, want reason code %q", err, serviceregistry.ReasonDesiredStateNotApplicable)
	}

	// The same command against a managed service is accepted.
	managed := managementTestHead("stackkits-inventory", "managed")
	prepared, err := prepareServiceEvent(managed, event, now)
	if err != nil {
		t.Fatalf("managed desired write: %v", err)
	}
	if prepared.head.Runtime.DesiredState != "stopped" {
		t.Fatalf("managed desired state = %q, want stopped", prepared.head.Runtime.DesiredState)
	}
}

// A Guard observation of an observed service must not smuggle an intent in, and
// must never append a `desired` row to the timeline for it.
func TestPrepareServiceEventIgnoresGuardDesiredSeedForObservedServices(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	prepared, err := prepareServiceEvent(nil, serviceEventTestCommand(ServiceRuntime{
		StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
		Source: "observed", DesiredState: "stopped", ObservedState: "running", HealthState: "healthy",
	}), now)
	if err != nil {
		t.Fatalf("prepareServiceEvent: %v", err)
	}
	for _, transition := range prepared.transitions {
		if transition.Dimension == serviceDimensionDesired {
			t.Fatalf("an observed service recorded a desired transition: %#v", transition)
		}
	}
	if prepared.head.Runtime.DesiredState != string(serviceregistry.DesiredRunning) {
		t.Fatalf("guard seeded an intent for an observed service: %q", prepared.head.Runtime.DesiredState)
	}
}

// Ownership and provenance are separate axes; a head that carries a value
// outside either vocabulary must never reach the database CHECKs.
func TestValidateServiceEventHeadRejectsOutOfVocabularyOwnershipAndSource(t *testing.T) {
	base := *managementTestHead("stackkits-inventory", "managed")
	base.Runtime.ManagementState = "adopted"
	if err := validateServiceEventHead(base); !errors.Is(err, ErrConflict) {
		t.Fatalf("management state error = %v, want ErrConflict", err)
	}

	base = *managementTestHead("stackkits-inventory", "managed")
	// The StackKits evidence-provenance vocabulary is a different axis.
	base.Runtime.Source = "verified-apply-evidence"
	if err := validateServiceEventHead(base); !errors.Is(err, ErrConflict) {
		t.Fatalf("source error = %v, want ErrConflict", err)
	}
}

// The memory adapter must resolve ownership with the same rule as the Postgres
// aggregate, or the two lanes would disagree about who owns a service.
func TestMemoryStoreResolvesServiceOwnershipLikeTheAggregate(t *testing.T) {
	store := NewMemoryStore()
	ctx := t.Context()

	for _, test := range []struct {
		name, source, status string
		metadata             map[string]any
		want                 string
	}{
		{name: "discovered", source: "observed", status: "running", want: "observed"},
		{name: "stackkit rollout", source: "stackkit_outputs", status: "running", want: "managed"},
		{name: "legacy observed status", source: "stackkit_outputs", status: "observed", want: "observed"},
		{
			name: "hand imported custom", source: "stackkit_outputs", status: "running",
			metadata: map[string]any{"type": "custom"}, want: "observed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved, err := store.UpsertService(ctx, Service{
				ID: "service-" + test.name, TenantID: "tenant-1", StackID: "stack-1",
				NodeID: "node-1", ServiceKey: "vaultwarden", Name: "Vaultwarden",
				Status: test.status, Source: test.source, Metadata: test.metadata,
			})
			if err != nil {
				t.Fatalf("UpsertService: %v", err)
			}
			if saved.ManagementState != test.want {
				t.Fatalf("management_state = %q, want %q", saved.ManagementState, test.want)
			}
		})
	}
}
