package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// One Guard observation carries both halves of the service model: services a
// StackKit declared and services merely discovered on the host. The batch
// source is the default, not a uniform one, or the unmanaged half could never
// be published at all.
func TestGuardInventoryProjectionKeepsPerServiceProvenance(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	observedAt := now.Add(time.Minute)
	command := guardInventoryProjectionTestCommand(observedAt, 1, true, "svc-managed", "svc-observed")
	for index := range command.Services {
		if command.Services[index].Legacy.ID != "svc-observed" {
			continue
		}
		command.Services[index].Legacy.Source = "observed"
		command.Services[index].Runtime.Source = "observed"
		// A discovered service declares no target state.
		command.Services[index].Runtime.DesiredState = ""
	}

	if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); err != nil {
		t.Fatalf("ApplyGuardInventoryProjection: %v", err)
	}
	for _, test := range []struct{ id, source, management string }{
		{id: "svc-managed", source: "stackkits-inventory", management: "managed"},
		{id: "svc-observed", source: "observed", management: "observed"},
	} {
		runtime, err := store.GetServiceRuntime(context.Background(), "tenant-1", test.id)
		if err != nil {
			t.Fatalf("GetServiceRuntime(%s): %v", test.id, err)
		}
		if runtime.Source != test.source || runtime.ManagementState != test.management {
			t.Fatalf("%s = source %q management %q, want %q/%q",
				test.id, runtime.Source, runtime.ManagementState, test.source, test.management)
		}
		legacy, err := store.GetService(context.Background(), "tenant-1", test.id)
		if err != nil {
			t.Fatalf("GetService(%s): %v", test.id, err)
		}
		if legacy.Source != runtime.Source || legacy.ManagementState != runtime.ManagementState {
			t.Fatalf("%s projections disagree on ownership: legacy=%#v runtime source=%q management=%q",
				test.id, legacy, runtime.Source, runtime.ManagementState)
		}
	}
}

// The authoritative prune belongs to the managed half. An observed row is not
// declared by the manifest, so a manifest that no longer lists it proves
// nothing about it and must not delete it.
func TestGuardInventoryManifestPruneLeavesObservedServicesAlone(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	first := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-managed", "svc-observed")
	for index := range first.Services {
		if first.Services[index].Legacy.ID != "svc-observed" {
			continue
		}
		first.Services[index].Legacy.Source = "observed"
		first.Services[index].Runtime.Source = "observed"
	}
	applied, err := store.ApplyGuardInventoryProjection(context.Background(), first)
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}

	// The manifest now lists only the managed service. The observed row must
	// survive: nothing in the manifest ever claimed it.
	second := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true, "svc-managed")
	second.Event.ExpectedRevision = applied.ServerEvent.Server.Revision
	if _, err := store.ApplyGuardInventoryProjection(context.Background(), second); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	if _, err := store.GetService(context.Background(), "tenant-1", "svc-observed"); err != nil {
		t.Fatalf("manifest prune deleted an observed service it never declared: %v", err)
	}
}

// Both projections of one physical row must agree, and a provenance outside
// the closed vocabulary is refused rather than folded: this boundary only ever
// sees values the control plane produced, so an unnameable one is a bug, not
// untrusted input.
func TestGuardInventoryServiceSourceResolution(t *testing.T) {
	for _, test := range []struct {
		name, legacy, runtime, batch, want, wantErr string
	}{
		{name: "unstated inherits the batch default", batch: "stackkits-inventory", want: "stackkits-inventory"},
		{name: "declared wins over the default", legacy: "observed", runtime: "observed", batch: "stackkits-inventory", want: "observed"},
		{name: "one-sided declaration is honored", runtime: "observed", batch: "stackkits-inventory", want: "observed"},
		{name: "disagreement is refused", legacy: "observed", runtime: "stackkits-inventory", batch: "stackkits-inventory", wantErr: "inconsistent source"},
		{name: "out-of-vocabulary is refused", legacy: "verified-apply-evidence", runtime: "verified-apply-evidence", batch: "stackkits-inventory", wantErr: "unknown service source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := guardInventoryServiceSource(
				Service{ID: "svc-a", Source: test.legacy},
				ServiceRuntime{ID: "svc-a", Source: test.runtime},
				test.batch,
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, test.wantErr)
				}
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("error = %v, want ErrConflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("guardInventoryServiceSource: %v", err)
			}
			if got != test.want {
				t.Fatalf("source = %q, want %q", got, test.want)
			}
		})
	}
}
