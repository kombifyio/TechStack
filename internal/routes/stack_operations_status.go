//nolint:goconst
package routes

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/pocketbase/pocketbase/core"
)

type stackReadiness struct {
	Status         string `json:"status"`
	CanStart       bool   `json:"can_start"`
	Required       int    `json:"required_servers"`
	Approved       int    `json:"approved_servers"`
	Connected      int    `json:"connected_servers"`
	Pending        int    `json:"pending_servers"`
	Assigned       int    `json:"assigned_servers"`
	Available      int    `json:"available_servers"`
	Unassigned     int    `json:"unassigned_servers"`
	Message        string `json:"message"`
	ReviewRequired bool   `json:"review_required"`
}

type stackNextStep struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Action      string `json:"action,omitempty"`
}

type stackReadinessInput struct {
	status         string
	required       int
	managedRuntime bool
	// failedJobType is the type of the newest failed job, when the stack is in
	// the error status. A stack lands in "error" for ANY failed job type, so
	// without it a failed teardown would be reported as a failed rollout.
	failedJobType string
}

type stackNextStepsInput struct {
	status             string
	managedRuntime     bool
	runtimePhase       string
	verificationStatus string
	leaseID            string
	stateAvailable     bool
}

type stackServerReadinessCounts struct {
	approved   int
	connected  int
	pending    int
	assigned   int
	available  int
	unassigned int
}

func buildStackReadiness(stack *core.Record, servers []stackOperationServer, failure *stackLatestFailure) stackReadiness {
	return buildStackReadinessFromInput(stackReadinessInput{
		status:         stack.GetString("status"),
		required:       requiredServersForStack(stack),
		managedRuntime: isManagedRuntimeStack(stack),
		failedJobType:  failedJobTypeOf(failure),
	}, servers)
}

func buildStackReadinessFromStore(stack *controlplane.Stack, servers []stackOperationServer, failure *stackLatestFailure) stackReadiness {
	input := stackReadinessInput{
		required:      requiredServersForStoreStack(stack),
		failedJobType: failedJobTypeOf(failure),
	}
	if stack != nil {
		input.status = stack.Status
		input.managedRuntime = isManagedRuntimeStoreStack(stack)
	}
	return buildStackReadinessFromInput(input, servers)
}

func failedJobTypeOf(failure *stackLatestFailure) string {
	if failure == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(failure.Type))
}

// failedOperationMessage names what actually failed. The stack status alone
// cannot distinguish a failed rollout from a failed teardown, and pointing the
// operator at a "creation job" for a destroy failure sends them to the wrong
// record entirely.
func failedOperationMessage(failedJobType string) string {
	switch failedJobType {
	case "destroy":
		return "Decommissioning this deployment failed. Open the latest destroy job for the concrete provider error."
	case "update":
		return "The last update job failed. Open it for the concrete error."
	case "deploy":
		return "The last rollout failed. Open the latest deploy job for the concrete runtime error."
	case "provision":
		return "Provisioning failed. Open the latest creation job for the concrete runtime error."
	default:
		return "The last operation failed. Open the latest job for the concrete error."
	}
}

func buildStackReadinessFromInput(input stackReadinessInput, servers []stackOperationServer) stackReadiness {
	counts := countStackServerReadiness(servers)
	readiness := stackReadiness{
		Status:         "waiting_for_servers",
		Required:       input.required,
		Approved:       counts.approved,
		Connected:      counts.connected,
		Pending:        counts.pending,
		Assigned:       counts.assigned,
		Available:      counts.available,
		Unassigned:     counts.unassigned,
		Message:        "Connect and confirm the required servers before rollout.",
		ReviewRequired: true,
	}
	if input.managedRuntime {
		readiness.ReviewRequired = false
		readiness.Message = "Managed kombify Cloud rollout runs automatically from the creation job."
	}
	// A connected user-owned server is still a valid rollout target after the
	// current deploy/provision attempt failed. The persisted stack status may
	// remain "running" because the Guard/server itself is healthy; that must not
	// hide the only way to start the StackKit rollout again.
	if !input.managedRuntime && (input.failedJobType == "deploy" || input.failedJobType == "provision") && counts.connected >= input.required {
		readiness.Status = "ready"
		readiness.CanStart = true
		readiness.ReviewRequired = true
		readiness.Message = "The last rollout failed. Review the configuration and start a fresh rollout when ready."
		return readiness
	}
	if input.status == "running" {
		readiness.Status = "running"
		readiness.Message = "Stack is running."
		readiness.ReviewRequired = false
		return readiness
	}
	if input.status == "provisioning" {
		readiness.Status = "busy"
		if input.managedRuntime {
			readiness.Message = "Managed VM lease and StackKit rollout are still in progress."
		} else {
			readiness.Message = "A rollout or provisioning job is already running."
		}
		return readiness
	}
	if input.managedRuntime {
		if input.status == "error" {
			readiness.Status = "error"
			readiness.Message = failedOperationMessage(input.failedJobType)
			return readiness
		}
		if counts.connected < input.required {
			readiness.Status = "waiting_for_managed_runtime"
			readiness.Message = "Waiting for the managed VM lease to enroll and expose a real runtime target."
			return readiness
		}
		readiness.Status = "ready"
		readiness.Message = "Managed runtime target is available; rollout status is driven by the creation job."
		return readiness
	}
	if counts.approved < input.required && counts.available > 0 {
		readiness.Status = "waiting_for_assignment"
		readiness.Message = "Assign available confirmed servers before rollout."
		return readiness
	}
	if counts.connected >= input.required {
		readiness.Status = "ready"
		readiness.CanStart = true
		readiness.Message = "Review the configuration and start the rollout when ready."
	}
	return readiness
}

func countStackServerReadiness(servers []stackOperationServer) stackServerReadinessCounts {
	counts := stackServerReadinessCounts{}
	now := time.Now().UTC()
	for _, server := range servers {
		if server.Assignment == "stack" {
			counts.assigned++
			if server.Approved {
				counts.approved++
			} else {
				counts.pending++
			}
			if stackOperationServerConnectedAt(server, now) {
				counts.connected++
			}
			continue
		}
		counts.unassigned++
		if server.Approved {
			counts.available++
		}
	}
	return counts
}

func stackOperationServerConnectedAt(server stackOperationServer, now time.Time) bool {
	if server.Assignment != "stack" || !server.Approved || server.heartbeatAt == nil || server.heartbeatAt.IsZero() {
		return false
	}
	if !server.observedAt.IsZero() {
		now = server.observedAt
	}
	age := now.UTC().Sub(server.heartbeatAt.UTC())
	if age < -30*time.Second || age > runtimehealth.FreshHeartbeatWindow {
		return false
	}
	connection := serverregistry.ConnectionState(strings.ToLower(strings.TrimSpace(server.Status)))
	if connection != serverregistry.ConnectionConnected && connection != serverregistry.ConnectionDegraded {
		return false
	}
	health := serverregistry.HealthState(strings.ToLower(strings.TrimSpace(server.Health.State)))
	return health == serverregistry.HealthHealthy || health == serverregistry.HealthDegraded
}

func requiredServersForStack(stack *core.Record) int {
	configs := make([]map[string]any, 0, 2)
	for _, field := range []string{"user_config", "config"} {
		config, ok := mapFromJSONAny(stack.Get(field))
		if !ok {
			continue
		}
		configs = append(configs, config)
	}
	return requiredServersFromConfigs(configs...)
}

func requiredServersForStoreStack(stack *controlplane.Stack) int {
	if stack == nil {
		return 1
	}
	return requiredServersFromConfigs(stack.Config, stack.RuntimeSummary)
}

func requiredServersFromConfigs(configs ...map[string]any) int {
	for _, config := range configs {
		if nodes := stringListFromAny(config["nodes"]); len(nodes) > 0 {
			return len(nodes)
		}
		if count, ok := numericInt(config["min_servers"]); ok && count > 0 {
			return count
		}
		if requirements, ok := mapFromJSONAny(config["requirements"]); ok {
			if count, ok := numericInt(requirements["min_total_servers"]); ok && count > 0 {
				return count
			}
		}
	}
	return 1
}

func numericInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		converted := int(v)
		return converted, int64(converted) == v
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func buildStackNextSteps(stack *core.Record, readiness stackReadiness) []stackNextStep {
	return buildStackNextStepsFromInput(stackNextStepsInput{
		status:             stack.GetString("status"),
		managedRuntime:     isManagedRuntimeStack(stack),
		runtimePhase:       stack.GetString("runtime_phase"),
		verificationStatus: stack.GetString("verification_status"),
		leaseID:            stack.GetString("lease_id"),
		stateAvailable:     true,
	}, readiness)
}

func buildStackNextStepsFromStore(stack *controlplane.Stack, readiness stackReadiness) []stackNextStep {
	input := stackNextStepsInput{stateAvailable: stack != nil}
	if stack != nil {
		input.status = stack.Status
		input.managedRuntime = isManagedRuntimeStoreStack(stack)
		input.runtimePhase = firstNonEmptyString(storeStackRuntimeField(stack, "runtime_phase"))
		input.verificationStatus = firstNonEmptyString(storeStackRuntimeField(stack, "verification_status"))
		input.leaseID = firstNonEmptyString(storeStackRuntimeField(stack, "lease_id"))
	}
	return buildStackNextStepsFromInput(input, readiness)
}

func buildStackNextStepsFromInput(input stackNextStepsInput, readiness stackReadiness) []stackNextStep {
	if input.managedRuntime {
		verified := input.status == "running" && input.verificationStatus == "verified" && input.runtimePhase == "verified"
		leaseReady := readiness.Connected >= readiness.Required && input.leaseID != ""
		rolloutDone := verified || input.runtimePhase == "deployed"
		return []stackNextStep{
			{
				ID:          "managed_runtime",
				Label:       "Provision Managed Runtime",
				Description: managedRuntimeStepDescription(input, readiness),
				Status:      stepStatus(leaseReady, input.status == "provisioning" || input.leaseID != ""),
			},
			{
				ID:          "stackkit_rollout",
				Label:       "Run StackKit rollout",
				Description: "Simulation, StackKit rollout, and verification run through Runtime Actions.",
				Status:      stepStatus(rolloutDone, input.status == "provisioning" && leaseReady),
			},
			{
				ID:          "owner_login",
				Label:       "Enable owner login",
				Description: "Owner access, Login Gateway, and recovery must come from StackKit outputs.",
				Status:      stepStatus(verified, input.status == "provisioning" && rolloutDone),
			},
			{
				ID:          "verify_monitoring",
				Label:       "Verify backups and monitoring",
				Description: "Restore drill and monitoring proofs must be verified by the rollout.",
				Status:      stepStatus(verified, input.status == "provisioning" && rolloutDone),
			},
		}
	}
	return []stackNextStep{
		{
			ID:          "review_config",
			Label:       "Review configuration",
			Description: "Review the StackKit standard, services, and server placement.",
			Status:      "completed",
		},
		{
			ID:          "connect_servers",
			Label:       "Register servers",
			Description: fmt.Sprintf("%d of %d servers have a current Guard heartbeat.", readiness.Connected, readiness.Required),
			Status:      stepStatus(readiness.Connected >= readiness.Required, input.status == "pending"),
		},
		{
			ID:          "start_rollout",
			Label:       "Start rollout",
			Description: "Starts orchestration explicitly through Review + Start.",
			Status:      stepStatus((input.status == "running" || input.status == "provisioning") && !readiness.CanStart, readiness.CanStart),
			Action:      "review_start",
		},
		{
			ID:          "owner_login",
			Label:       "Enable owner login",
			Description: "Verify Pocket ID and owner access after the rollout.",
			Status:      stepStatus(input.status == "running" && !readiness.CanStart, false),
		},
		{
			ID:          "verify_monitoring",
			Label:       "Verify backups and monitoring",
			Description: "Verify health KPIs, alerts, and baseline backup protection.",
			Status:      stepStatus(input.status == "running" && !readiness.CanStart, false),
		},
	}
}

func isManagedRuntimeStack(stack *core.Record) bool {
	if stack == nil {
		return false
	}
	return isManagedRuntimeState(
		stack.GetString("server_provisioning_mode"),
		stack.GetString("server_mode"),
		stack.GetString("runtime_lane"),
		stack.GetString("lease_id"),
	)
}

func isManagedRuntimeStoreStack(stack *controlplane.Stack) bool {
	if stack == nil {
		return false
	}
	return isManagedRuntimeState(
		storeStackRuntimeField(stack, "server_provisioning_mode"),
		storeStackRuntimeField(stack, "server_mode"),
		storeStackRuntimeField(stack, "runtime_lane"),
		storeStackRuntimeField(stack, "lease_id"),
	)
}

func isManagedRuntimeState(serverProvisioningMode, serverMode, runtimeLane, leaseID string) bool {
	switch strings.ToLower(strings.TrimSpace(serverProvisioningMode)) {
	case "kombify-cloud":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(serverMode)) {
	case "monthly-runtime", "managed-cloud":
		return true
	}
	return strings.EqualFold(runtimeLane, "monthly-runtime") || strings.TrimSpace(leaseID) != ""
}

func managedRuntimeStepDescription(input stackNextStepsInput, readiness stackReadiness) string {
	if !input.stateAvailable {
		return "VM lease and enrollment are read from real runtime data."
	}
	leaseID := strings.TrimSpace(input.leaseID)
	if leaseID == "" {
		return "The VM lease has not been persisted by the Creation Job yet."
	}
	return fmt.Sprintf("Lease %s: %d of %d Managed Runtime targets are ready.", leaseID, readiness.Connected, readiness.Required)
}

func storeStackRuntimeField(stack *controlplane.Stack, key string) string {
	if stack == nil {
		return ""
	}
	return firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, key), stringFromAnyMap(stack.Config, key))
}

func stepStatus(done bool, current bool) string {
	if done {
		return "completed"
	}
	if current {
		return "current"
	}
	return "pending"
}
