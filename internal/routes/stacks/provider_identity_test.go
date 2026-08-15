package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestResolveCreateProviderIdentityRequiresOneExactCanonicalSelection(t *testing.T) {
	tests := []struct {
		name    string
		req     createStackRequest
		want    string
		wantErr error
	}{
		{
			name: "canonical values across all documented locations agree",
			req: createStackRequest{
				ProviderID: providercatalog.ProviderIONOS,
				Options:    map[string]interface{}{providercatalog.ProviderIDField: providercatalog.ProviderIONOS},
				StackSpec: map[string]interface{}{
					providercatalog.ProviderIDField: providercatalog.ProviderIONOS,
					"metadata":                      map[string]interface{}{providercatalog.ProviderIDField: providercatalog.ProviderIONOS},
					"nodes":                         []interface{}{map[string]interface{}{providercatalog.ProviderIDField: providercatalog.ProviderIONOS}},
				},
				UserConfigRaw: `{"options":{"provider_id":"ionos"}}`,
			},
			want: providercatalog.ProviderIONOS,
		},
		{
			name:    "cloud UI mode requires an explicit provider id",
			req:     createStackRequest{Provider: "cloud"},
			wantErr: providercatalog.ErrProviderIDRequired,
		},
		{
			name: "managed mode without provider selection fails",
			req: createStackRequest{StackSpec: map[string]interface{}{
				"metadata": map[string]interface{}{"server_mode": runtimeModeMonthlyRuntime},
			}},
			wantErr: providercatalog.ErrProviderIDRequired,
		},
		{
			name: "conflicting canonical values fail",
			req: createStackRequest{
				ProviderID: providercatalog.ProviderCentron,
				StackSpec:  map[string]interface{}{providercatalog.ProviderIDField: providercatalog.ProviderIONOS},
			},
			wantErr: providercatalog.ErrConflictingProviderIDs,
		},
		{
			name:    "composite provider id is never mapped",
			req:     createStackRequest{ProviderID: "ionos-managed"},
			wantErr: providercatalog.ErrCompositeProviderID,
		},
		{
			name:    "composite provider mode is never mapped",
			req:     createStackRequest{Provider: "ionos-managed"},
			wantErr: providercatalog.ErrCompositeProviderID,
		},
		{
			name: "managed node cannot replace provider id",
			req: createStackRequest{StackSpec: map[string]interface{}{
				"metadata": map[string]interface{}{"server_mode": runtimeModeMonthlyRuntime},
				"nodes":    []interface{}{map[string]interface{}{"provider": providercatalog.ProviderIONOS}},
			}},
			wantErr: providercatalog.ErrProviderIDRequired,
		},
		{
			name: "managed node must match provider id",
			req: createStackRequest{ProviderID: providercatalog.ProviderIONOS, StackSpec: map[string]interface{}{
				"nodes": []interface{}{map[string]interface{}{"provider": providercatalog.ProviderCentron}},
			}},
			wantErr: providercatalog.ErrConflictingProviderIDs,
		},
		{
			name:    "case and whitespace are not normalized",
			req:     createStackRequest{ProviderID: " ionos "},
			wantErr: providercatalog.ErrUnsupportedProviderID,
		},
		{
			name:    "provider mode case is detected but not normalized",
			req:     createStackRequest{Provider: "IONOS"},
			wantErr: providercatalog.ErrUnsupportedProviderID,
		},
		{
			name:    "provider mode whitespace is detected but not normalized",
			req:     createStackRequest{Provider: " ionos "},
			wantErr: providercatalog.ErrUnsupportedProviderID,
		},
		{
			name:    "legacy field in raw node fails",
			req:     createStackRequest{UserConfigRaw: `{"provider":"cloud","nodes":[{"simulate_provider_id":"ionos-managed"}]}`},
			wantErr: providercatalog.ErrLegacyProviderWriteField,
		},
		{
			name: "non-managed config needs no provider id",
			req: createStackRequest{Provider: "local", StackSpec: map[string]interface{}{
				"context": "cloud",
				"nodes":   []interface{}{map[string]interface{}{"provider": "hetzner"}},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveCreateProviderIdentity(test.req)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("resolveCreateProviderIdentity() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("resolveCreateProviderIdentity() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeCreateStackRequestPersistsOnlyCanonicalProviderID(t *testing.T) {
	normalized, msg := normalizeCreateStackRequest(createStackRequest{
		Provider:   "cloud",
		ProviderID: providercatalog.ProviderCentron,
		StackSpec: map[string]interface{}{
			"provider": "cloud",
			"metadata": map[string]interface{}{
				"server_mode":                   runtimeModeMonthlyRuntime,
				providercatalog.ProviderIDField: providercatalog.ProviderCentron,
			},
		},
	})
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q", msg)
	}
	if normalized.ProviderID != providercatalog.ProviderCentron {
		t.Fatalf("ProviderID = %q, want %q", normalized.ProviderID, providercatalog.ProviderCentron)
	}
	for name, config := range map[string]map[string]interface{}{
		"user config": normalized.UserConfig,
		"job spec":    createStackJobSpec(normalized),
		"runtime":     runtimeFieldsFromConfig(runtimePolicyConfigFromRequest(normalized)),
	} {
		if got := stringFromAny(config[providercatalog.ProviderIDField]); got != providercatalog.ProviderCentron {
			t.Fatalf("%s provider_id = %q, want %q", name, got, providercatalog.ProviderCentron)
		}
		if config[providercatalog.LegacyLeaseProviderField] != nil || config[providercatalog.LegacySimulateProviderIDField] != nil {
			t.Fatalf("%s contains legacy provider fields: %#v", name, config)
		}
	}
}

func TestCreateStackRejectsLegacyProviderBeforeAnyPersistentWrite(t *testing.T) {
	store := controlplane.NewMemoryStore()
	body, err := json.Marshal(createStackRequest{
		Name:          "legacy-provider",
		Mode:          stackModeEasy,
		Provider:      "cloud",
		LeaseProvider: "ionos-managed",
		StackSpec:     map[string]interface{}{"provider": "cloud"},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", strings.NewReader(string(body)))
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: request, Response: recorder}

	if err := (crudRouteHandlers{stackStore: store, jobStore: store, serverStore: store, deploymentMode: config.ModeSaaS}).createStack(event); err != nil {
		t.Fatalf("createStack() router error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	stacks, err := store.ListStacksByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacks) != 0 {
		t.Fatalf("stacks = %#v, want no write", stacks)
	}
}
