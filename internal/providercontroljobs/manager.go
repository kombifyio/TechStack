package providercontroljobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

const (
	desiredSpecSchemaVersion = "techstack/provider-desired-spec/v2"
	defaultOfferingID        = "monthly-runtime-standard"
)

var (
	providerIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	runtimeSlotKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// ErrNativeDecommissionUnavailable is returned only after an activation
	// gate opens without a native decommission application being composed.
	ErrNativeDecommissionUnavailable = errors.New("providercontroljobs: native decommission application unavailable")
	// ErrNativeProvisionReplayFailed prevents a later job retry from presenting
	// an already-terminal provider operation as a successful allocation.
	ErrNativeProvisionReplayFailed = errors.New("providercontroljobs: native provision replay is terminal")
)

// ProvisionAdmission is the narrow native admission application used by the
// job adapter. *providercontrol.NativeAdmission satisfies it.
type ProvisionAdmission interface {
	ResolveManagedRuntimeSlotGeneration(
		context.Context, string, string, string,
	) (providercontrol.ManagedRuntimeSlotGenerationResolution, error)
	PreflightProvision(context.Context, providercontrol.NativeProvisionAdmissionRequest) error
	AdmitProvision(context.Context, providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error)
}

func (m *Manager) ResolveManagedRuntimeSlotGeneration(
	ctx context.Context,
	request jobs.ManagedRuntimeSlotGenerationRequest,
) (jobs.ManagedRuntimeSlotGeneration, error) {
	if m == nil || nilManagerDependency(m.admission) {
		return jobs.ManagedRuntimeSlotGeneration{}, fmt.Errorf("providercontroljobs: manager is not configured")
	}
	slotKey, err := normalizeRuntimeSlotKey(request.RuntimeSlotKey)
	if err != nil || strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.StackID) == "" {
		return jobs.ManagedRuntimeSlotGeneration{}, fmt.Errorf("providercontroljobs: canonical runtime slot identity is required")
	}
	resolved, err := m.admission.ResolveManagedRuntimeSlotGeneration(
		ctx, strings.TrimSpace(request.TenantID), strings.TrimSpace(request.StackID), slotKey,
	)
	if err != nil {
		return jobs.ManagedRuntimeSlotGeneration{}, err
	}
	if resolved.RuntimeSlotID == "" || resolved.GenerationOrdinal == 0 {
		return jobs.ManagedRuntimeSlotGeneration{}, fmt.Errorf("providercontroljobs: runtime slot generation resolution is incomplete")
	}
	return jobs.ManagedRuntimeSlotGeneration{
		RuntimeSlotID: resolved.RuntimeSlotID, GenerationOrdinal: resolved.GenerationOrdinal,
		ExistingUnreleased: resolved.ExistingUnreleased,
	}, nil
}

// ManagerConfig supplies the activation gate and native application seams.
type ManagerConfig struct {
	Admission      ProvisionAdmission
	ActivationGate providercontrol.MutationActivationGate
	Decommissioner jobs.ManagedLeaseDecommissioner
	AsyncRequests  *providercontrol.AsyncRequestStore
}

// Manager adapts managed-runtime jobs to native provider-control admission.
type Manager struct {
	admission      ProvisionAdmission
	activation     providercontrol.MutationActivationGate
	decommissioner jobs.ManagedLeaseDecommissioner
	asyncRequests  *providercontrol.AsyncRequestStore
}

// NewManager constructs the job adapter. NativeAdmission owns the provision
// gate so it can resolve durable replays before checking fresh activation; the
// explicit gate remains mandatory for native decommission delegation.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if nilManagerDependency(cfg.Admission) || nilManagerDependency(cfg.ActivationGate) {
		return nil, fmt.Errorf("providercontroljobs: admission and activation gate are required")
	}
	if nilManagerDependency(cfg.Decommissioner) {
		cfg.Decommissioner = nil
	}
	return &Manager{
		admission: cfg.Admission, activation: cfg.ActivationGate,
		decommissioner: cfg.Decommissioner, asyncRequests: cfg.AsyncRequests,
	}, nil
}

// PreflightCreateOrBindLease performs the native admission application's
// read-only availability check before an HTTP route persists or queues a job.
// It deliberately returns no grant or token; CreateOrBindLease repeats the
// authoritative checks while atomically reserving capacity and admission state.
func (m *Manager) PreflightCreateOrBindLease(
	ctx context.Context,
	req jobs.ManagedLeaseRequest,
) error {
	if m == nil || nilManagerDependency(m.admission) || nilManagerDependency(m.activation) {
		return fmt.Errorf("providercontroljobs: manager is not configured")
	}
	normalized, err := normalizeManagedLeaseRequest(req)
	if err != nil {
		return err
	}
	return m.admission.PreflightProvision(ctx, normalized.admission)
}

// CreateOrBindLease atomically admits a native lease/server/operation intent.
// It never calls a provider adapter; the provider-control worker owns later
// reconciliation after the admission transaction commits.
func (m *Manager) CreateOrBindLease(
	ctx context.Context,
	req jobs.ManagedLeaseRequest,
) (*jobs.ManagedLeaseResult, error) {
	if m == nil || nilManagerDependency(m.admission) || nilManagerDependency(m.activation) {
		return nil, fmt.Errorf("providercontroljobs: manager is not configured")
	}
	normalized, err := normalizeManagedLeaseRequest(req)
	if err != nil {
		return nil, err
	}
	// NativeAdmission owns the provision activation gate after its durable
	// replay lookup. Rechecking the gate here would make an already committed
	// same-key replay unavailable if activation closes later.
	result, err := m.admission.AdmitProvision(ctx, normalized.admission)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.LeaseID) == "" || result.LeaseID != string(normalized.admission.Lease.ID) ||
		result.RuntimeSlotKey != normalized.admission.RuntimeSlotKey ||
		result.RuntimeSlotID != normalized.admission.RuntimeSlotID ||
		result.RuntimeSlotGeneration != normalized.admission.RuntimeSlotGeneration ||
		strings.TrimSpace(result.RuntimeServerID) == "" || result.RuntimeServerID != normalized.admission.RuntimeServerID ||
		strings.TrimSpace(result.ResourceGenerationID) == "" ||
		strings.TrimSpace(result.Operation.Command.OperationID) == "" {
		return nil, fmt.Errorf("providercontroljobs: native admission returned incomplete or substituted correlations")
	}
	if result.Operation.Head.Status == providerexecutor.StatusFailed ||
		result.Operation.Head.Phase == providerexecutor.PhaseFailed {
		reasonCode := ""
		if result.Operation.Head.Reason != nil {
			reasonCode = strings.TrimSpace(result.Operation.Head.Reason.Code)
		}
		if reasonCode == "" {
			reasonCode = "provider.unknown"
		}
		providerDetail := ""
		if m.asyncRequests != nil {
			if request, ok, loadErr := m.asyncRequests.Load(ctx,
				result.Operation.Command.TenantID, result.Operation.Command.OperationID); loadErr == nil && ok && request.FailureMessage != "" {
				providerDetail = fmt.Sprintf("; provider request %s: %s",
					firstNonEmptyProviderDetail(request.FailureCode, reasonCode), request.FailureMessage)
			}
		}
		return nil, fmt.Errorf(
			"%w: operation %s failed with reason %s%s",
			ErrNativeProvisionReplayFailed,
			result.Operation.Command.OperationID,
			reasonCode,
			providerDetail,
		)
	}
	phase := jobs.RuntimePhaseLeasePending
	if result.Operation.Head.Status == providerexecutor.StatusSucceeded &&
		result.Operation.Head.Phase == providerexecutor.PhasePresent {
		phase = jobs.RuntimePhaseLeaseReady
	}
	return &jobs.ManagedLeaseResult{
		RuntimeSlotKey:        result.RuntimeSlotKey,
		RuntimeSlotID:         result.RuntimeSlotID,
		RuntimeSlotGeneration: result.RuntimeSlotGeneration,
		LeaseID:               result.LeaseID,
		RuntimeServerID:       result.RuntimeServerID,
		ResourceGenerationID:  result.ResourceGenerationID,
		OperationID:           result.Operation.Command.OperationID,
		Provider:              normalized.providerID,
		DesiredState:          string(vmlease.DesiredStateRunning),
		BillingMode:           string(vmlease.BillingModeSubscription),
		Phase:                 phase,
		IdempotentReplay:      !result.Created,
	}, nil
}

func firstNonEmptyProviderDetail(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "provider.unknown"
}

// DecommissionManagedLeases delegates only after the same immutable
// activation gate has opened. Production composes the native implementation;
// the nil branch remains a fail-closed composition guard for tests and
// disabled provider bundles.
func (m *Manager) ManagedLeaseDecommissionReady(ctx context.Context) error {
	if m == nil || nilManagerDependency(m.activation) {
		return fmt.Errorf("providercontroljobs: manager is not configured")
	}
	if err := m.activation.Require(ctx); err != nil {
		return err
	}
	if m.decommissioner == nil {
		return ErrNativeDecommissionUnavailable
	}
	return nil
}

func (m *Manager) DecommissionManagedLeases(
	ctx context.Context,
	req jobs.ManagedLeaseDecommissionRequest,
) (*jobs.ManagedLeaseDecommissionResult, error) {
	if err := m.ManagedLeaseDecommissionReady(ctx); err != nil {
		return nil, err
	}
	return m.decommissioner.DecommissionManagedLeases(ctx, req)
}

type normalizedManagedLeaseRequest struct {
	providerID string
	admission  providercontrol.NativeProvisionAdmissionRequest
}

type publicDesiredSpec struct {
	SchemaVersion         string   `json:"schema_version"`
	ProviderID            string   `json:"provider_id"`
	OfferingID            string   `json:"offering_id"`
	Region                string   `json:"region,omitempty"`
	StackID               string   `json:"stack_id"`
	StackName             string   `json:"stack_name"`
	StackKit              string   `json:"stackkit"`
	NodeRole              string   `json:"node_role"`
	RuntimeSlotKey        string   `json:"runtime_slot_key"`
	RuntimeSlotGeneration uint64   `json:"runtime_slot_generation"`
	Services              []string `json:"services"`
}

func normalizeManagedLeaseRequest(req jobs.ManagedLeaseRequest) (normalizedManagedLeaseRequest, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	ownerID := strings.TrimSpace(req.OwnerID)
	stackID := strings.TrimSpace(req.StackID)
	stackName := strings.TrimSpace(req.StackName)
	stackKit := strings.TrimSpace(req.StackKit)
	if _, err := normalizeOperationKey(req.OperationKey); err != nil {
		return normalizedManagedLeaseRequest{}, err
	}
	nodeRole, err := normalizeNodeRole(req.NodeRole)
	if err != nil {
		return normalizedManagedLeaseRequest{}, err
	}
	runtimeSlotKey, err := normalizeRuntimeSlotKey(req.RuntimeSlotKey)
	if err != nil {
		return normalizedManagedLeaseRequest{}, err
	}
	runtimeSlotGeneration := req.RuntimeSlotGeneration
	if runtimeSlotGeneration == 0 {
		runtimeSlotGeneration = 1
	}
	generationKey := strconv.FormatUint(runtimeSlotGeneration, 10)
	services, err := normalizeServices(req.Services)
	if err != nil {
		return normalizedManagedLeaseRequest{}, err
	}
	providerID, err := providercatalog.CanonicalProviderID(req.Provider)
	if err != nil {
		return normalizedManagedLeaseRequest{}, fmt.Errorf("providercontroljobs: canonical provider_id is required: %w", err)
	}
	if tenantID == "" || ownerID == "" || stackID == "" || stackName == "" || stackKit == "" {
		return normalizedManagedLeaseRequest{}, fmt.Errorf("providercontroljobs: tenant, owner, stack id, stack name, and stackkit are required")
	}
	offeringID := strings.TrimSpace(req.Metadata["runtime_offering_id"])
	if offeringID == "" {
		offeringID = defaultOfferingID
	}
	if !providerIDPattern.MatchString(offeringID) {
		return normalizedManagedLeaseRequest{}, fmt.Errorf("providercontroljobs: canonical offering_id is required")
	}
	region := strings.TrimSpace(req.Metadata["provider_region"])
	if len(region) > 128 || strings.ContainsAny(region, "\r\n\x00") {
		return normalizedManagedLeaseRequest{}, fmt.Errorf("providercontroljobs: invalid provider region")
	}

	runtimeSlotID := providercontrol.DeriveManagedRuntimeSlotID(tenantID, stackID, runtimeSlotKey)
	leaseID := vmlease.LeaseID(stableID("lease", tenantID, stackID, runtimeSlotKey, generationKey))
	serverID := runtimeidentity.LeaseServerID(string(leaseID))
	serverName := managedRuntimeServerName(stackName, nodeRole, tenantID, stackID, runtimeSlotKey, runtimeSlotGeneration)
	leaseMetadata := map[string]string{
		"stack_id": stackID, "stack_name": stackName, "stackkit": stackKit,
		"runtime_slot_key": runtimeSlotKey, "runtime_slot_id": runtimeSlotID,
		"runtime_slot_generation": generationKey,
		"runtime_offering_id":     offeringID, "node_role": nodeRole,
		"server_node_role": nodeRole, "requested_services": strings.Join(services, ","),
		monthlyruntime.MetadataKeyRuntimeLane: serverruntime.RuntimeLaneMonthly,
		monthlyruntime.MetadataKeyServerMode:  serverruntime.RuntimeLaneMonthly,
		monthlyruntime.MetadataKeyBillingMode: monthlyruntime.BillingSubscription,
	}
	if region != "" {
		leaseMetadata["provider_region"] = region
	}
	lease := vmlease.Lease{
		ID:           leaseID,
		Subject:      vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:     vmlease.ResourceRef{ProviderID: providerID, Region: region},
		DesiredState: vmlease.DesiredStateRunning, BillingMode: vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		Metadata:       leaseMetadata,
	}
	specJSON, err := json.Marshal(publicDesiredSpec{
		SchemaVersion: desiredSpecSchemaVersion, ProviderID: providerID,
		OfferingID: offeringID, Region: region, StackID: stackID,
		StackName: stackName, StackKit: stackKit, NodeRole: nodeRole,
		RuntimeSlotKey:        runtimeSlotKey,
		RuntimeSlotGeneration: runtimeSlotGeneration,
		Services:              services,
	})
	if err != nil {
		return normalizedManagedLeaseRequest{}, err
	}
	idempotencyKey := stableID("provision", tenantID, stackID, runtimeSlotKey, generationKey)
	return normalizedManagedLeaseRequest{
		providerID: providerID,
		admission: providercontrol.NativeProvisionAdmissionRequest{
			TenantID: tenantID, RuntimeSlotKey: runtimeSlotKey, RuntimeSlotID: runtimeSlotID,
			RuntimeSlotGeneration: runtimeSlotGeneration,
			RuntimeServerID:       serverID, OwnerSubjectID: ownerID,
			IdempotencyKey: idempotencyKey, Lease: lease,
			Server: providercontrol.NativeAdmissionServer{
				StackID: stackID, Name: serverName,
				Metadata: map[string]any{
					"provider_id": providerID, "offering_id": offeringID, "stackkit": stackKit,
					"node_role": nodeRole, "runtime_slot_key": runtimeSlotKey,
					"runtime_slot_id":         runtimeSlotID,
					"runtime_slot_generation": runtimeSlotGeneration,
					"services":                services,
				},
			},
			DesiredSpecRef: "desired-spec://techstack/leases/" + string(leaseID) + "/revisions/1",
			DesiredSpec:    specJSON,
		},
	}, nil
}

func normalizeRuntimeSlotKey(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = jobs.PrimaryManagedRuntimeSlotKey
	}
	if !runtimeSlotKeyPattern.MatchString(value) {
		return "", fmt.Errorf("providercontroljobs: canonical runtime slot key is required")
	}
	return value, nil
}

func normalizeOperationKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(trimmed) > 256 || containsControl(trimmed) {
		return "", fmt.Errorf("providercontroljobs: valid operation key is required")
	}
	return trimmed, nil
}

func normalizeNodeRole(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "foundation", "main":
		return "foundation", nil
	case "worker":
		return "worker", nil
	case "storage":
		return "storage", nil
	default:
		return "", fmt.Errorf("providercontroljobs: canonical node role is required")
	}
}

func normalizeServices(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, fmt.Errorf("providercontroljobs: too many requested services")
	}
	seen := make(map[string]struct{}, len(values))
	services := make([]string, 0, len(values))
	for _, value := range values {
		service := strings.ToLower(strings.TrimSpace(value))
		service = strings.ReplaceAll(service, "-", "_")
		switch service {
		case "pocketid", "pocket_id", "pocketbase_identity", "identity":
			service = "pocket_id"
		}
		if service == "" {
			continue
		}
		if len(service) > 128 || strings.Contains(service, ",") || containsControl(service) ||
			!providerIDPattern.MatchString(service) {
			return nil, fmt.Errorf("providercontroljobs: invalid requested service")
		}
		if _, exists := seen[service]; exists {
			continue
		}
		seen[service] = struct{}{}
		services = append(services, service)
	}
	sort.Strings(services)
	if services == nil {
		services = []string{}
	}
	return services, nil
}

func managedRuntimeServerName(
	stackName, nodeRole, tenantID, stackID, runtimeSlotKey string,
	generation uint64,
) string {
	if runtimeSlotKey == jobs.PrimaryManagedRuntimeSlotKey && generation == 1 {
		return stackName
	}
	digest := strings.TrimPrefix(stableID(
		"node", tenantID, stackID, runtimeSlotKey, strconv.FormatUint(generation, 10),
	), "node-")
	if len(digest) > 8 {
		digest = digest[:8]
	}
	return strings.Trim(strings.Join([]string{stackName, nodeRole, digest}, "-"), "-")
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func nilManagerDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func stableID(prefix string, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(hash.Sum(nil)[:16])
}

var (
	_ jobs.ManagedLeaseManager                  = (*Manager)(nil)
	_ jobs.ManagedLeaseAdmissionPreflighter     = (*Manager)(nil)
	_ jobs.ManagedRuntimeSlotGenerationResolver = (*Manager)(nil)
	_ jobs.ManagedLeaseDecommissioner           = (*Manager)(nil)
)
