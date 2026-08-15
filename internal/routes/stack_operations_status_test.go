package routes

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/pocketbase/pocketbase/core"
)

func TestStackOperationsStatusLegacyStoreParity(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name            string
		status          string
		failedJobType   string
		runtime         map[string]string
		servers         []stackOperationServer
		wantReadiness   string
		wantMessage     string
		wantCanStart    bool
		wantReview      bool
		wantApproved    int
		wantConnected   int
		wantNextStepIDs []stackStatusTestStep
	}{
		{
			name:          "running",
			status:        "running",
			servers:       []stackOperationServer{connectedStackStatusTestServer(now)},
			wantReadiness: "running",
			wantMessage:   "Stack is running.",
			wantReview:    false,
			wantApproved:  1,
			wantConnected: 1,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "review_config", Status: "completed"},
				{ID: "connect_servers", Status: "completed"},
				{ID: "start_rollout", Status: "completed", Action: "review_start"},
				{ID: "owner_login", Status: "completed"},
				{ID: "verify_monitoring", Status: "completed"},
			},
		},
		{
			name:          "provisioning",
			status:        "provisioning",
			servers:       []stackOperationServer{{Assignment: "stack"}},
			wantReadiness: "busy",
			wantMessage:   "A rollout or provisioning job is already running.",
			wantReview:    true,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "review_config", Status: "completed"},
				{ID: "connect_servers", Status: "pending"},
				{ID: "start_rollout", Status: "completed", Action: "review_start"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:          "managed error names the failed provision",
			status:        "error",
			runtime:       map[string]string{"server_mode": "managed-cloud"},
			failedJobType: "provision",
			wantReadiness: "error",
			wantMessage:   "Provisioning failed. Open the latest creation job for the concrete runtime error.",
			wantReview:    false,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "managed_runtime", Status: "pending"},
				{ID: "stackkit_rollout", Status: "pending"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			// A stack lands in "error" for ANY failed job type. Reporting a
			// failed teardown as a failed rollout sends the operator to the
			// wrong job record.
			name:          "managed error names the failed teardown",
			status:        "error",
			runtime:       map[string]string{"server_mode": "managed-cloud"},
			failedJobType: "destroy",
			wantReadiness: "error",
			wantMessage:   "Decommissioning this deployment failed. Open the latest destroy job for the concrete provider error.",
			wantReview:    false,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "managed_runtime", Status: "pending"},
				{ID: "stackkit_rollout", Status: "pending"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:          "managed wait",
			status:        "pending",
			runtime:       map[string]string{"server_provisioning_mode": "kombify-cloud"},
			servers:       []stackOperationServer{{Assignment: "available", Approved: true}},
			wantReadiness: "waiting_for_managed_runtime",
			wantMessage:   "Waiting for the managed VM lease to enroll and expose a real runtime target.",
			wantReview:    false,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "managed_runtime", Status: "pending"},
				{ID: "stackkit_rollout", Status: "pending"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:   "managed ready",
			status: "pending",
			runtime: map[string]string{
				"runtime_lane": "monthly-runtime",
				"lease_id":     "lease-123",
			},
			servers:       []stackOperationServer{connectedStackStatusTestServer(now)},
			wantReadiness: "ready",
			wantMessage:   "Managed runtime target is available; rollout status is driven by the creation job.",
			wantCanStart:  false,
			wantReview:    false,
			wantApproved:  1,
			wantConnected: 1,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "managed_runtime", Status: "completed"},
				{ID: "stackkit_rollout", Status: "pending"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:          "waiting for assignment",
			status:        "pending",
			servers:       []stackOperationServer{{Assignment: "available", Approved: true}},
			wantReadiness: "waiting_for_assignment",
			wantMessage:   "Assign available confirmed servers before rollout.",
			wantReview:    true,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "review_config", Status: "completed"},
				{ID: "connect_servers", Status: "current"},
				{ID: "start_rollout", Status: "pending", Action: "review_start"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:          "self hosted ready",
			status:        "pending",
			servers:       []stackOperationServer{connectedStackStatusTestServer(now)},
			wantReadiness: "ready",
			wantMessage:   "Review the configuration and start the rollout when ready.",
			wantCanStart:  true,
			wantReview:    true,
			wantApproved:  1,
			wantConnected: 1,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "review_config", Status: "completed"},
				{ID: "connect_servers", Status: "completed"},
				{ID: "start_rollout", Status: "current", Action: "review_start"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
		{
			name:          "self hosted connected after failed rollout remains restartable",
			status:        "running",
			failedJobType: "deploy",
			servers:       []stackOperationServer{connectedStackStatusTestServer(now)},
			wantReadiness: "ready",
			wantMessage:   "The last rollout failed. Review the configuration and start a fresh rollout when ready.",
			wantCanStart:  true,
			wantReview:    true,
			wantApproved:  1,
			wantConnected: 1,
			wantNextStepIDs: []stackStatusTestStep{
				{ID: "review_config", Status: "completed"},
				{ID: "connect_servers", Status: "completed"},
				{ID: "start_rollout", Status: "current", Action: "review_start"},
				{ID: "owner_login", Status: "pending"},
				{ID: "verify_monitoring", Status: "pending"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacy := newStackStatusTestLegacyStack(t, tt.status, nil, nil, tt.runtime)
			store := newStackStatusTestStoreStack(tt.status, nil, nil, tt.runtime)

			var failure *stackLatestFailure
			if tt.failedJobType != "" {
				failure = &stackLatestFailure{Type: tt.failedJobType, State: "failed"}
			}
			legacyReadiness := buildStackReadiness(legacy, tt.servers, failure)
			storeReadiness := buildStackReadinessFromStore(store, tt.servers, failure)
			if !reflect.DeepEqual(legacyReadiness, storeReadiness) {
				t.Fatalf("readiness parity mismatch:\nlegacy = %#v\nstore  = %#v", legacyReadiness, storeReadiness)
			}
			if legacyReadiness.Status != tt.wantReadiness || legacyReadiness.Message != tt.wantMessage || legacyReadiness.CanStart != tt.wantCanStart || legacyReadiness.ReviewRequired != tt.wantReview || legacyReadiness.Approved != tt.wantApproved || legacyReadiness.Connected != tt.wantConnected {
				t.Fatalf("readiness = %#v, want status=%q message=%q canStart=%v reviewRequired=%v approved=%d connected=%d", legacyReadiness, tt.wantReadiness, tt.wantMessage, tt.wantCanStart, tt.wantReview, tt.wantApproved, tt.wantConnected)
			}

			legacySteps := buildStackNextSteps(legacy, legacyReadiness)
			storeSteps := buildStackNextStepsFromStore(store, storeReadiness)
			if !reflect.DeepEqual(legacySteps, storeSteps) {
				t.Fatalf("next-step parity mismatch:\nlegacy = %#v\nstore  = %#v", legacySteps, storeSteps)
			}
			if got := projectStackStatusTestSteps(legacySteps); !reflect.DeepEqual(got, tt.wantNextStepIDs) {
				t.Fatalf("next-step id/order/status/action = %#v, want %#v", got, tt.wantNextStepIDs)
			}
		})
	}
}

func TestStackOperationServerConnectedAtRequiresCanonicalFreshHealthyGuardEvidence(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-runtimehealth.FreshHeartbeatWindow / 2)
	stale := now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second)
	future := now.Add(31 * time.Second)
	base := stackOperationServer{
		Assignment:  "stack",
		Approved:    true,
		Status:      "connected",
		Health:      stackServerHealth{State: "healthy"},
		heartbeatAt: &fresh,
	}
	tests := []struct {
		name   string
		server stackOperationServer
		want   bool
	}{
		{name: "fresh healthy canonical heartbeat", server: base, want: true},
		{name: "fresh degraded canonical heartbeat", server: func() stackOperationServer {
			server := base
			server.Status = "degraded"
			server.Health.State = "degraded"
			return server
		}(), want: true},
		{name: "worker last seen text is not canonical evidence", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = nil
			server.LastSeen = fresh.Format(time.RFC3339Nano)
			return server
		}()},
		{name: "missing heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = nil
			return server
		}()},
		{name: "stale heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = &stale
			return server
		}()},
		{name: "implausible future heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = &future
			return server
		}()},
		{name: "unassigned", server: func() stackOperationServer {
			server := base
			server.Assignment = "available"
			return server
		}()},
		{name: "not approved", server: func() stackOperationServer {
			server := base
			server.Approved = false
			return server
		}()},
		{name: "invalid connection", server: func() stackOperationServer {
			server := base
			server.Status = "provisioned"
			return server
		}()},
		{name: "invalid health", server: func() stackOperationServer {
			server := base
			server.Health.State = "unknown"
			return server
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stackOperationServerConnectedAt(test.server, now); got != test.want {
				t.Fatalf("stackOperationServerConnectedAt() = %v, want %v for %#v", got, test.want, test.server)
			}
		})
	}

	boundaryHeartbeat := now.Add(-runtimehealth.FreshHeartbeatWindow + time.Millisecond)
	boundary := base
	boundary.heartbeatAt = &boundaryHeartbeat
	boundary.observedAt = now
	if !stackOperationServerConnectedAt(boundary, now.Add(time.Second)) {
		t.Fatal("readiness must use the canonical projection timestamp instead of aging within one response")
	}
}

func TestRequiredServersLegacyStorePrecedence(t *testing.T) {
	tests := []struct {
		name      string
		primary   map[string]any
		secondary map[string]any
		want      int
	}{
		{
			name: "default",
			want: 1,
		},
		{
			name: "nodes before min servers and requirements",
			primary: map[string]any{
				"nodes":       []string{"main", "worker"},
				"min_servers": 7,
				"requirements": map[string]any{
					"min_total_servers": 9,
				},
			},
			want: 2,
		},
		{
			name: "min servers before requirements",
			primary: map[string]any{
				"min_servers": float64(3),
				"requirements": map[string]any{
					"min_total_servers": 8,
				},
			},
			want: 3,
		},
		{
			name: "requirements fallback",
			primary: map[string]any{
				"requirements": map[string]any{
					"min_total_servers": json.Number("4"),
				},
			},
			want: 4,
		},
		{
			name: "primary config before secondary config",
			primary: map[string]any{
				"requirements": map[string]any{"min_total_servers": 2},
			},
			secondary: map[string]any{
				"nodes": []string{"one", "two", "three", "four", "five"},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacy := newStackStatusTestLegacyStack(t, "pending", tt.primary, tt.secondary, nil)
			store := newStackStatusTestStoreStack("pending", tt.primary, tt.secondary, nil)
			if got := requiredServersForStack(legacy); got != tt.want {
				t.Fatalf("legacy required servers = %d, want %d", got, tt.want)
			}
			if got := requiredServersForStoreStack(store); got != tt.want {
				t.Fatalf("store required servers = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestManagedRuntimeStepDescription(t *testing.T) {
	readiness := stackReadiness{Approved: 2, Connected: 1, Required: 2}
	tests := []struct {
		name  string
		input stackNextStepsInput
		want  string
	}{
		{
			name: "state unavailable",
			want: "VM lease and enrollment are read from real runtime data.",
		},
		{
			name:  "lease not persisted",
			input: stackNextStepsInput{stateAvailable: true},
			want:  "The VM lease has not been persisted by the Creation Job yet.",
		},
		{
			name:  "lease ready",
			input: stackNextStepsInput{stateAvailable: true, leaseID: " lease-123 "},
			want:  "Lease lease-123: 1 of 2 Managed Runtime targets are ready.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managedRuntimeStepDescription(tt.input, readiness); got != tt.want {
				t.Fatalf("managed runtime step description = %q, want %q", got, tt.want)
			}
		})
	}
}

func connectedStackStatusTestServer(observedAt time.Time) stackOperationServer {
	heartbeatAt := observedAt.UTC()
	return stackOperationServer{
		Assignment:  "stack",
		Approved:    true,
		Status:      "connected",
		Health:      stackServerHealth{State: "healthy"},
		heartbeatAt: &heartbeatAt,
	}
}

type stackStatusTestStep struct {
	ID     string
	Status string
	Action string
}

func projectStackStatusTestSteps(steps []stackNextStep) []stackStatusTestStep {
	result := make([]stackStatusTestStep, 0, len(steps))
	for _, step := range steps {
		result = append(result, stackStatusTestStep{ID: step.ID, Status: step.Status, Action: step.Action})
	}
	return result
}

func newStackStatusTestLegacyStack(t *testing.T, status string, primary, secondary map[string]any, runtime map[string]string) *core.Record {
	t.Helper()
	collection := core.NewBaseCollection("stack_status_test")
	collection.Fields.Add(
		&core.TextField{Name: "status"},
		&core.TextField{Name: "runtime_phase"},
		&core.TextField{Name: "server_mode"},
		&core.TextField{Name: "runtime_lane"},
		&core.TextField{Name: "lease_id"},
		&core.TextField{Name: "verification_status"},
		&core.TextField{Name: "server_provisioning_mode"},
		&core.JSONField{Name: "user_config"},
		&core.JSONField{Name: "config"},
	)
	record := core.NewRecord(collection)
	record.Set("status", status)
	if primary != nil {
		record.Set("user_config", primary)
	}
	if secondary != nil {
		record.Set("config", secondary)
	}
	for key, value := range runtime {
		record.Set(key, value)
	}
	return record
}

func newStackStatusTestStoreStack(status string, primary, secondary map[string]any, runtime map[string]string) *controlplane.Stack {
	runtimeSummary := copyStackStatusTestMap(secondary)
	for key, value := range runtime {
		runtimeSummary[key] = value
	}
	return &controlplane.Stack{
		Status:         status,
		Config:         copyStackStatusTestMap(primary),
		RuntimeSummary: runtimeSummary,
	}
}

func copyStackStatusTestMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
