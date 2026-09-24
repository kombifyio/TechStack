package controlplane

import (
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// TestServiceMutationLockIsControlPlaneAuthorityOnly is the authority boundary
// of the guardrail. Guard reports what it measures; an owner guardrail is not a
// measurement, so a Guard event that asserts one is rejected outright rather
// than silently ignored - a silently dropped lock command is indistinguishable
// from an applied one at the call site.
func TestServiceMutationLockIsControlPlaneAuthorityOnly(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	current := serviceEventTestHead("running", "running", "healthy", "running")

	guardCommand := serviceEventTestCommand(ServiceRuntime{
		MutationLock: ServiceMutationLock{State: serviceregistry.MutationLocked},
	})
	if _, err := prepareServiceEvent(current, guardCommand, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("Guard lock assertion error = %v, want ErrConflict", err)
	}

	// The same patch from the control plane is accepted and produces exactly one
	// lock transition.
	ownerCommand := serviceEventTestCommand(ServiceRuntime{
		MutationLock: ServiceMutationLock{
			State: serviceregistry.MutationLocked, ReasonCode: "owner_locked", Actor: "owner-1",
		},
	})
	ownerCommand.Authority = ServiceEventAuthorityControlPlane
	ownerCommand.Source = "owner-action"
	prepared, err := prepareServiceEvent(current, ownerCommand, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.transitions) != 1 || prepared.transitions[0].Dimension != serviceDimensionLock ||
		prepared.transitions[0].ToState != string(serviceregistry.MutationLocked) {
		t.Fatalf("lock transitions = %#v", prepared.transitions)
	}
	if !prepared.head.Runtime.MutationLock.Locked() ||
		prepared.head.Runtime.MutationLock.ChangedAt == nil {
		t.Fatalf("stored lock = %#v", prepared.head.Runtime.MutationLock)
	}
	// The guardrail never touches a measured dimension.
	if prepared.head.Runtime.ObservedState != current.Runtime.ObservedState ||
		prepared.head.Runtime.HealthState != current.Runtime.HealthState {
		t.Fatalf("lock changed measured state: %#v", prepared.head.Runtime)
	}
}

// TestServiceMutationLockRejectsUnknownStates keeps the vocabulary closed at the
// write boundary. A value we cannot interpret must not be stored as if it were
// a guardrail.
func TestServiceMutationLockRejectsUnknownStates(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	command := serviceEventTestCommand(ServiceRuntime{
		MutationLock: ServiceMutationLock{State: serviceregistry.MutationLockState("quarantined")},
	})
	command.Authority = ServiceEventAuthorityControlPlane
	command.Source = "owner-action"
	if _, err := prepareServiceEvent(serviceEventTestHead("running", "running", "healthy", "running"), command, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown lock state error = %v, want ErrConflict", err)
	}
}

// TestServiceMutationUnlockClearsItsExplanation proves an unlocked service
// carries no stale "locked by X" residue, which would be a lie in every read
// model that renders it.
func TestServiceMutationUnlockClearsItsExplanation(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	locked := serviceEventTestHead("running", "running", "healthy", "running")
	stamp := now.Add(-time.Hour)
	locked.Runtime.MutationLock = ServiceMutationLock{
		State: serviceregistry.MutationLocked, ReasonCode: "owner_locked",
		Actor: "owner-1", ChangedAt: &stamp,
	}
	command := serviceEventTestCommand(ServiceRuntime{
		MutationLock: ServiceMutationLock{State: serviceregistry.MutationUnlocked},
	})
	command.Authority = ServiceEventAuthorityControlPlane
	command.Source = "owner-action"
	prepared, err := prepareServiceEvent(locked, command, now)
	if err != nil {
		t.Fatal(err)
	}
	lock := prepared.head.Runtime.MutationLock
	if lock.Locked() || lock.ReasonCode != "" || lock.Actor != "" || lock.ChangedAt != nil {
		t.Fatalf("unlock left residue: %#v", lock)
	}
}
