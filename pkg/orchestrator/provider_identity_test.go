package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestCanonicalizeProvisionProviderIdentityRejectsLegacyAndConflictingInputs(t *testing.T) {
	tests := []struct {
		name        string
		spec        map[string]interface{}
		stackConfig map[string]interface{}
		wantErr     error
	}{
		{
			name: "historical alias cannot authorize a new operation",
			stackConfig: map[string]interface{}{
				"server_mode":                            "monthly-runtime",
				providercatalog.LegacyLeaseProviderField: "ionos-managed",
			},
			wantErr: providercatalog.ErrLegacyProviderWriteField,
		},
		{
			name: "legacy field in raw imported spec fails",
			spec: map[string]interface{}{
				"user_config_raw": `metadata: {simulate_provider_id: centron-managed}`,
			},
			wantErr: providercatalog.ErrLegacyProviderWriteField,
		},
		{
			name: "different explicit canonical values fail",
			spec: map[string]interface{}{
				providercatalog.ProviderIDField: providercatalog.ProviderIONOS,
				"nodes":                         []interface{}{map[string]interface{}{providercatalog.ProviderIDField: providercatalog.ProviderCentron}},
			},
			wantErr: providercatalog.ErrConflictingProviderIDs,
		},
		{
			name:    "composite provider mode fails before writes",
			spec:    map[string]interface{}{"provider": "ionos-managed"},
			wantErr: providercatalog.ErrCompositeProviderID,
		},
		{
			name: "managed node cannot replace provider id",
			spec: map[string]interface{}{
				"metadata": map[string]interface{}{"server_mode": runtimeLaneMonthly},
				"nodes":    []interface{}{map[string]interface{}{"provider": providercatalog.ProviderIONOS}},
			},
			wantErr: providercatalog.ErrProviderIDRequired,
		},
		{
			name:    "provider mode case is detected but not normalized",
			spec:    map[string]interface{}{"provider": "IONOS"},
			wantErr: providercatalog.ErrUnsupportedProviderID,
		},
		{
			name:    "provider mode whitespace is detected but not normalized",
			spec:    map[string]interface{}{"provider": " ionos "},
			wantErr: providercatalog.ErrUnsupportedProviderID,
		},
		{
			name: "cloud context with unmanaged provider remains unmanaged",
			spec: map[string]interface{}{
				"context": "cloud",
				"nodes":   []interface{}{map[string]interface{}{"provider": "hetzner"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := canonicalizeProvisionProviderIdentity(test.spec, test.stackConfig)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("canonicalizeProvisionProviderIdentity() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCanonicalizeProvisionProviderIdentityRejectsCloudWithoutExplicitProvider(t *testing.T) {
	spec := map[string]interface{}{"provider": "cloud"}
	_, err := canonicalizeProvisionProviderIdentity(spec, nil)
	if !errors.Is(err, providercatalog.ErrProviderIDRequired) {
		t.Fatalf("canonicalizeProvisionProviderIdentity() error = %v, want provider_id required", err)
	}
	if _, mutated := spec[providercatalog.ProviderIDField]; mutated {
		t.Fatalf("caller spec was mutated: %#v", spec)
	}
}

func TestProvisionStackWithOptionsRejectsHistoricalAliasBeforeJobOrStatusWrite(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-historical",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Historical",
		Mode:           "easy",
		Status:         persistentStatePending,
		Config: map[string]any{
			"server_mode":                            runtimeLaneMonthly,
			providercatalog.LegacyLeaseProviderField: "ionos-managed",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	defer orch.Stop()

	_, err := orch.ProvisionStackWithOptions("stack-historical", nil, ProvisionStackOptions{
		TenantID: "tenant-1",
		OwnerID:  "owner-1",
	})
	if !errors.Is(err, providercatalog.ErrLegacyProviderWriteField) {
		t.Fatalf("ProvisionStackWithOptions() error = %v, want legacy provider rejection", err)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-historical")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != persistentStatePending {
		t.Fatalf("stack status = %q, want unchanged %q", stack.Status, persistentStatePending)
	}
	jobs, err := store.ListJobsByStack(ctx, "tenant-1", "stack-historical", 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want no persistent write", jobs)
	}
}
