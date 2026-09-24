package providercontrol

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func validatePreparedProvisionRequest(
	prepared PreparedProvisionRequest,
	invocation AdapterInvocation,
	expectedAdapterManifestHash string,
) (PreparedProvisionBinding, error) {
	if prepared == nil || nilPreparedProvisionRequest(prepared) {
		return PreparedProvisionBinding{}, fmt.Errorf("%w: prepared provision request is required", ErrAdapterSafety)
	}
	binding := prepared.ProvisionBinding()
	if err := validatePreparedProvisionBinding(binding, invocation, expectedAdapterManifestHash); err != nil {
		return PreparedProvisionBinding{}, err
	}
	return binding, nil
}

func validatePreparedProvisionBinding(
	binding PreparedProvisionBinding,
	invocation AdapterInvocation,
	expectedAdapterManifestHash string,
) error {
	for _, field := range []struct{ name, value string }{
		{"request_digest", binding.RequestDigest},
		{"credential_version_hash", binding.CredentialVersionHash},
		{"provider_scope_hash", binding.ProviderScopeHash},
		{"correlation_hash", binding.CorrelationHash},
		{"adapter_manifest_hash", binding.AdapterManifestHash},
	} {
		if !executionProfileDigestPattern.MatchString(field.value) {
			return fmt.Errorf(
				"%w: prepared provision %s must be a lowercase sha256 digest", ErrAdapterSafety, field.name,
			)
		}
	}
	command := invocation.Request.Command
	if binding.CredentialVersionHash != command.CustodyHash ||
		binding.ProviderScopeHash != command.ConnectionHash ||
		binding.CorrelationHash != sha256Digest([]byte(invocation.CorrelationID)) ||
		binding.AdapterManifestHash != expectedAdapterManifestHash {
		return fmt.Errorf(
			"%w: prepared provision custody, scope, correlation, or adapter manifest binding changed", ErrAdapterSafety,
		)
	}
	return nil
}

func preparedProvisionBindingDigest(binding PreparedProvisionBinding) string {
	return sha256Digest([]byte(strings.Join([]string{
		"providercontrol/prepared-provision-binding/v1",
		binding.RequestDigest,
		binding.CredentialVersionHash,
		binding.ProviderScopeHash,
		binding.CorrelationHash,
		binding.AdapterManifestHash,
	}, "\x00")))
}

func nilPreparedProvisionRequest(prepared PreparedProvisionRequest) bool {
	value := reflect.ValueOf(prepared)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateProvisionDispatchMode(operation providerexecutor.Operation, mode ProvisionDispatchMode) error {
	if admittedProvisionDispatchMode(mode) {
		return nil
	}
	if mode == ProvisionDispatchBlocked {
		return fmt.Errorf("%w: provision dispatch mode is quarantined", ErrAdapterSafety)
	}
	return fmt.Errorf("%w: unknown provision dispatch mode %q for %s", ErrAdapterSafety, mode, operation)
}

func admittedProvisionDispatchMode(mode ProvisionDispatchMode) bool {
	switch mode {
	case ProvisionDispatchNativeIdempotency,
		ProvisionDispatchProviderCorrelation,
		ProvisionDispatchAtMostOnceManualReconcile:
		return true
	default:
		return false
	}
}

func deriveOperationAutomationState(record OperationRecord, guardedCurrentHead bool) OperationAutomationState {
	if isTerminal(record.Head) {
		return OperationAutomationComplete
	}
	if record.Command.Operation == providerexecutor.OperationProvision &&
		record.Head.Status == providerexecutor.StatusPending &&
		record.Head.Phase == providerexecutor.PhaseAccepted && guardedCurrentHead {
		return OperationAutomationManualReconcileRequired
	}
	return OperationAutomationRunnable
}

func deriveOperationAutomationReasonCode(record OperationRecord, guardedCurrentHead bool) string {
	if deriveOperationAutomationState(record, guardedCurrentHead) == OperationAutomationManualReconcileRequired {
		// A guarded, handle-free accepted AMO provision is outcome-unknown even
		// if the process stopped before it could classify the transport result.
		// Conservatively expose the provider-neutral partial-create reason after
		// restart without claiming that a resource definitely exists.
		return providerexecutor.ReasonCodeProviderPartialCreate
	}
	return ""
}

func validateAtMostOnceProvisionDispatchResult(
	previous providerexecutor.Receipt,
	result providerexecutor.ExecutionResult,
) error {
	if previous.Operation != providerexecutor.OperationProvision ||
		previous.Status != providerexecutor.StatusPending ||
		previous.Phase != providerexecutor.PhaseAccepted || len(previous.Resources) != 0 {
		return fmt.Errorf("%w: AMO dispatch requires a handle-free pending accepted head", ErrInvalidRequest)
	}
	if result.Status == providerexecutor.StatusFailed &&
		result.Phase == providerexecutor.PhaseFailed {
		// The wire contract requires ambiguous failures to remain durable even
		// when the provider returned no handle. This terminalizes dispatch
		// automation without claiming absence or releasing capacity.
		return nil
	}
	if result.Status != providerexecutor.StatusPending {
		return fmt.Errorf("%w: AMO dispatch returned an unsupported terminal result", ErrCleanupCustody)
	}
	switch result.Phase {
	case providerexecutor.PhaseAccepted:
		if len(result.Resources) != 0 {
			return fmt.Errorf("%w: no-progress AMO dispatch cannot carry provider handles", ErrCleanupCustody)
		}
		return nil
	case providerexecutor.PhaseResourcesBound:
		if len(result.Resources) == 0 {
			return fmt.Errorf("%w: resources-bound AMO dispatch requires a nonempty provider resource graph", ErrCleanupCustody)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w: AMO dispatch may only remain accepted or bind resources", ErrCleanupCustody,
		)
	}
}

func validateProvisionPollIdentity(previous providerexecutor.Receipt, result providerexecutor.ExecutionResult) error {
	if previous.Operation != providerexecutor.OperationProvision || previous.Phase != providerexecutor.PhaseResourcesBound {
		return fmt.Errorf("%w: provision polling requires a resources-bound head", ErrInvalidRequest)
	}
	return validateMonotonicProvisionResourceIdentity(previous.Resources, result.Resources)
}

func validateDecommissionPollIdentity(previous providerexecutor.Receipt, result providerexecutor.ExecutionResult) error {
	if previous.Operation != providerexecutor.OperationDecommission ||
		(previous.Phase != providerexecutor.PhaseDeleteAccepted && previous.Phase != providerexecutor.PhaseAbsencePending) {
		return fmt.Errorf("%w: decommission polling requires a delete-accepted or absence-pending head", ErrInvalidRequest)
	}
	return validateReadOnlyResourceIdentity(previous.Resources, result.Resources, "decommission polling")
}

// validateReconcileDispatchResult bounds the single side-effecting reconcile
// step. The contract already confines every intermediate receipt to the
// command's target subset; this additionally forbids an accepted-head result
// from binding an empty graph or carrying handles without progress.
func validateReconcileDispatchResult(
	previous providerexecutor.Receipt,
	result providerexecutor.ExecutionResult,
) error {
	if previous.Operation != providerexecutor.OperationReconcile ||
		previous.Status != providerexecutor.StatusPending ||
		previous.Phase != providerexecutor.PhaseAccepted {
		return fmt.Errorf("%w: reconcile dispatch requires a pending accepted head", ErrInvalidRequest)
	}
	switch {
	case result.Status == providerexecutor.StatusFailed && result.Phase == providerexecutor.PhaseFailed:
		return nil
	case result.Status == providerexecutor.StatusPending && result.Phase == providerexecutor.PhaseAccepted:
		if len(result.Resources) != 0 {
			return fmt.Errorf("%w: no-progress reconcile dispatch cannot carry provider handles", ErrCleanupCustody)
		}
		return nil
	case result.Status == providerexecutor.StatusPending && result.Phase == providerexecutor.PhaseResourcesBound:
		if len(result.Resources) == 0 {
			return fmt.Errorf("%w: reconcile dispatch must retain the exact target graph", ErrCleanupCustody)
		}
		return nil
	default:
		return fmt.Errorf("%w: reconcile dispatch may only stay accepted, bind its targets, or fail", ErrCleanupCustody)
	}
}

func validateReconcilePollIdentity(previous providerexecutor.Receipt, result providerexecutor.ExecutionResult) error {
	if previous.Operation != providerexecutor.OperationReconcile || previous.Phase != providerexecutor.PhaseResourcesBound {
		return fmt.Errorf("%w: reconcile polling requires a resources-bound head", ErrInvalidRequest)
	}
	return validateReadOnlyResourceIdentity(previous.Resources, result.Resources, "reconcile polling")
}

func validateReadOnlyResourceIdentity(previous, next []providerexecutor.ResourceBinding, path string) error {
	if len(previous) != len(next) {
		return fmt.Errorf("%w: read-only %s changed the resource identity set", ErrCleanupCustody, path)
	}
	identities := make(map[string]providerResourceIdentity, len(previous))
	for _, resource := range previous {
		identities[resource.BindingID] = resourceIdentity(resource)
	}
	for _, resource := range next {
		identity, exists := identities[resource.BindingID]
		if !exists || identity != resourceIdentity(resource) {
			return fmt.Errorf("%w: read-only %s changed resource %q", ErrCleanupCustody, path, resource.BindingID)
		}
		delete(identities, resource.BindingID)
	}
	if len(identities) != 0 {
		return fmt.Errorf("%w: read-only %s omitted a resource identity", ErrCleanupCustody, path)
	}
	return nil
}

// Provision polling may discover child handles which were not expanded in an
// asynchronous provider's 202 response. Every previously bound identity must
// remain byte-stable and each new handle must descend from an existing root;
// unrelated roots or identity replacement remain forbidden.
func validateMonotonicProvisionResourceIdentity(previous, next []providerexecutor.ResourceBinding) error {
	if len(previous) == 0 || len(next) < len(previous) {
		return fmt.Errorf("%w: provision polling removed the durable resource root", ErrCleanupCustody)
	}
	previousIdentities := make(map[string]providerResourceIdentity, len(previous))
	for _, resource := range previous {
		if _, duplicate := previousIdentities[resource.BindingID]; duplicate {
			return fmt.Errorf("%w: provision polling received duplicate previous resource %q", ErrCleanupCustody, resource.BindingID)
		}
		previousIdentities[resource.BindingID] = resourceIdentity(resource)
	}
	nextByID := make(map[string]providerexecutor.ResourceBinding, len(next))
	for _, resource := range next {
		if _, duplicate := nextByID[resource.BindingID]; duplicate {
			return fmt.Errorf("%w: provision polling returned duplicate resource %q", ErrCleanupCustody, resource.BindingID)
		}
		nextByID[resource.BindingID] = resource
	}
	for bindingID, identity := range previousIdentities {
		resource, exists := nextByID[bindingID]
		if !exists || identity != resourceIdentity(resource) {
			return fmt.Errorf("%w: provision polling changed resource %q", ErrCleanupCustody, bindingID)
		}
	}
	for bindingID := range nextByID {
		if _, existed := previousIdentities[bindingID]; existed {
			continue
		}
		if !resourceDescendsFromPrevious(bindingID, nextByID, previousIdentities) {
			return fmt.Errorf("%w: provision polling added unrelated resource %q", ErrCleanupCustody, bindingID)
		}
	}
	return nil
}

func resourceDescendsFromPrevious(
	bindingID string,
	nextByID map[string]providerexecutor.ResourceBinding,
	previous map[string]providerResourceIdentity,
) bool {
	seen := map[string]struct{}{bindingID: {}}
	current := nextByID[bindingID]
	for current.ParentBindingID != "" {
		parentID := current.ParentBindingID
		if _, anchored := previous[parentID]; anchored {
			return true
		}
		if _, cycle := seen[parentID]; cycle {
			return false
		}
		seen[parentID] = struct{}{}
		parent, exists := nextByID[parentID]
		if !exists {
			return false
		}
		current = parent
	}
	return false
}

type providerResourceIdentity struct {
	bindingID       string
	kind            string
	nativeRef       string
	parentBindingID string
	ownershipHash   string
	disposition     providerexecutor.ResourceDisposition
}

func resourceIdentity(resource providerexecutor.ResourceBinding) providerResourceIdentity {
	return providerResourceIdentity{
		bindingID:       resource.BindingID,
		kind:            resource.Kind,
		nativeRef:       resource.NativeRef,
		parentBindingID: resource.ParentBindingID,
		ownershipHash:   resource.OwnershipHash,
		disposition:     resource.Disposition,
	}
}
