package providercontrol

import (
	"context"
	"fmt"
	"strings"
)

// ProvisionResolutionProvider contains only the provider-specific read and
// evidence-verification seams needed by Operator Reconcile. Workflow ordering,
// replay keys, revisions, and custody mutations stay in providercontrol.
type ProvisionResolutionProvider interface {
	ProvisionResolutionVerifier
	BuildProvisionDiscoveryRequest(
		context.Context, OperationRecord, uint64, string, string,
	) (RecordProvisionDiscoveryRequest, error)
}

type provisionResolutionOperationLoader interface {
	LoadOperation(context.Context, string, string) (OperationRecord, error)
}

// ProvisionResolutionApplication is the single provider-neutral application
// boundary for resolving one guarded at-most-once provision. Implementations
// may observe provider state but can never retry provider creation.
type ProvisionResolutionApplication interface {
	ResolveProvisionOperation(context.Context, ProvisionResolutionWorkflowRequest) (ProvisionResolutionWorkflowResult, error)
}

// ProvisionResolutionWorkflowRequest is the operator identity and exact
// confirmation for one already-parked provision operation. Provider handles,
// candidate counts, revisions, and replay keys are never caller supplied.
type ProvisionResolutionWorkflowRequest struct {
	TenantID          string
	OperationID       string
	OperatorSubjectID string
	Confirmation      string
}

// ProvisionDiscoveryWorkflowRequest identifies the first, read-only operator
// step. Provider handles, candidate counts, outcomes, and revisions are never
// accepted from the caller.
type ProvisionDiscoveryWorkflowRequest struct {
	TenantID          string
	OperationID       string
	OperatorSubjectID string
}

// ProvisionDiscoveryWorkflowResult reports the immutable provider-derived
// observation and whether this call appended it.
type ProvisionDiscoveryWorkflowResult struct {
	Observation ProvisionDiscoveryObservation
	Operation   OperationRecord
	Created     bool
}

// ProvisionResolutionWorkflowResult reports the immutable decision and the
// resulting provider-control operation head.
type ProvisionResolutionWorkflowResult struct {
	Observation      ProvisionDiscoveryObservation
	Decision         ProvisionResolutionDecision
	Operation        OperationRecord
	DiscoveryCreated bool
	DecisionCreated  bool
}

// ProvisionResolutionWorkflow is the single provider-neutral orchestration
// path for zero/one/many candidate resolution. It cannot dispatch, retry, or
// delete a provider resource.
type ProvisionResolutionWorkflow struct {
	operations provisionResolutionOperationLoader
	store      ProvisionResolutionStore
	provider   ProvisionResolutionProvider
}

func NewProvisionResolutionWorkflow(
	operations provisionResolutionOperationLoader,
	store ProvisionResolutionStore,
	provider ProvisionResolutionProvider,
) (*ProvisionResolutionWorkflow, error) {
	if operations == nil || store == nil || provider == nil {
		return nil, fmt.Errorf("%w: operation loader, resolution store, and provider are required", ErrInvalidRequest)
	}
	return &ProvisionResolutionWorkflow{operations: operations, store: store, provider: provider}, nil
}

func (w *ProvisionResolutionWorkflow) Resolve(
	ctx context.Context,
	request ProvisionResolutionWorkflowRequest,
) (ProvisionResolutionWorkflowResult, error) {
	if w == nil || w.operations == nil || w.store == nil || w.provider == nil {
		return ProvisionResolutionWorkflowResult{}, fmt.Errorf("%w: provision resolution workflow is not configured", ErrInvalidRequest)
	}
	tenantID := strings.TrimSpace(request.TenantID)
	operationID := strings.TrimSpace(request.OperationID)
	operatorSubjectID := strings.TrimSpace(request.OperatorSubjectID)
	confirmation := strings.TrimSpace(request.Confirmation)
	if tenantID == "" || operationID == "" || operatorSubjectID == "" || confirmation == "" {
		return ProvisionResolutionWorkflowResult{}, fmt.Errorf("%w: tenant, operation, operator, and confirmation are required", ErrInvalidRequest)
	}

	discovery, err := w.Discover(ctx, ProvisionDiscoveryWorkflowRequest{
		TenantID: tenantID, OperationID: operationID, OperatorSubjectID: operatorSubjectID,
	})
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	return w.commit(ctx, discovery.Operation, discovery.Observation, operatorSubjectID, confirmation, discovery.Created)
}

// Discover performs only the certified provider read and immutable evidence
// append. A replay reverifies the same observation and never replaces it.
func (w *ProvisionResolutionWorkflow) Discover(
	ctx context.Context,
	request ProvisionDiscoveryWorkflowRequest,
) (ProvisionDiscoveryWorkflowResult, error) {
	if w == nil || w.operations == nil || w.store == nil || w.provider == nil {
		return ProvisionDiscoveryWorkflowResult{}, fmt.Errorf("%w: provision resolution workflow is not configured", ErrInvalidRequest)
	}
	tenantID := strings.TrimSpace(request.TenantID)
	operationID := strings.TrimSpace(request.OperationID)
	operatorSubjectID := strings.TrimSpace(request.OperatorSubjectID)
	if tenantID == "" || operationID == "" || operatorSubjectID == "" {
		return ProvisionDiscoveryWorkflowResult{}, fmt.Errorf("%w: tenant, operation, and operator are required", ErrInvalidRequest)
	}
	record, err := w.operations.LoadOperation(ctx, tenantID, operationID)
	if err != nil {
		return ProvisionDiscoveryWorkflowResult{}, err
	}
	revision, err := w.store.LoadProvisionResolutionRevision(ctx, tenantID, operationID)
	if err != nil {
		return ProvisionDiscoveryWorkflowResult{}, err
	}
	revisionKey := fmt.Sprintf("%d", revision)
	discoveryKey := "provision-discovery:" + operationID + ":" + revisionKey
	observation, found, err := w.store.LoadProvisionDiscovery(ctx, tenantID, operationID, discoveryKey)
	if err != nil {
		return ProvisionDiscoveryWorkflowResult{}, err
	}
	discoveryCreated := false
	if found {
		// Resume never replaces immutable discovery. Reverification proves the
		// same provider graph is still current before the decision CAS.
		if err := w.provider.VerifyProvisionDiscovery(ctx, record, observation); err != nil {
			return ProvisionDiscoveryWorkflowResult{}, provisionResolutionVerificationError(ctx, err)
		}
	} else {
		discoveryRequest, buildErr := w.provider.BuildProvisionDiscoveryRequest(
			ctx, record, revision, operatorSubjectID, discoveryKey,
		)
		if buildErr != nil {
			return ProvisionDiscoveryWorkflowResult{}, buildErr
		}
		observation, discoveryCreated, err = w.store.RecordProvisionDiscovery(ctx, discoveryRequest)
		if err != nil {
			return ProvisionDiscoveryWorkflowResult{}, err
		}
	}
	return ProvisionDiscoveryWorkflowResult{Observation: observation, Operation: record, Created: discoveryCreated}, nil
}

// Commit resolves only an already-persisted discovery observation. Hosted
// transports use this separately confirmed second step.
func (w *ProvisionResolutionWorkflow) Commit(
	ctx context.Context,
	request ProvisionResolutionWorkflowRequest,
) (ProvisionResolutionWorkflowResult, error) {
	if w == nil || w.operations == nil || w.store == nil || w.provider == nil {
		return ProvisionResolutionWorkflowResult{}, fmt.Errorf("%w: provision resolution workflow is not configured", ErrInvalidRequest)
	}
	tenantID := strings.TrimSpace(request.TenantID)
	operationID := strings.TrimSpace(request.OperationID)
	operatorSubjectID := strings.TrimSpace(request.OperatorSubjectID)
	confirmation := strings.TrimSpace(request.Confirmation)
	if tenantID == "" || operationID == "" || operatorSubjectID == "" || confirmation == "" {
		return ProvisionResolutionWorkflowResult{}, fmt.Errorf("%w: tenant, operation, operator, and confirmation are required", ErrInvalidRequest)
	}
	record, err := w.operations.LoadOperation(ctx, tenantID, operationID)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	revision, err := w.store.LoadProvisionResolutionRevision(ctx, tenantID, operationID)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	revisionKey := fmt.Sprintf("%d", revision)
	discoveryKey := "provision-discovery:" + operationID + ":" + revisionKey
	observation, found, err := w.store.LoadProvisionDiscovery(ctx, tenantID, operationID, discoveryKey)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	if !found {
		return ProvisionResolutionWorkflowResult{}, fmt.Errorf("%w: provision discovery must be recorded before resolution", ErrInvalidRequest)
	}
	if err := w.provider.VerifyProvisionDiscovery(ctx, record, observation); err != nil {
		return ProvisionResolutionWorkflowResult{}, provisionResolutionVerificationError(ctx, err)
	}
	return w.commit(ctx, record, observation, operatorSubjectID, confirmation, false)
}

func (w *ProvisionResolutionWorkflow) commit(
	ctx context.Context,
	record OperationRecord,
	observation ProvisionDiscoveryObservation,
	operatorSubjectID, confirmation string,
	discoveryCreated bool,
) (ProvisionResolutionWorkflowResult, error) {
	revisionKey := fmt.Sprintf("%d", observation.ObservedResolutionRevision)
	operationID := observation.OperationID

	resolutionRequest, err := BuildProvisionResolutionRequest(
		record,
		observation,
		operatorSubjectID,
		"provision-resolution:"+operationID+":"+revisionKey,
		confirmation,
	)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	decision, current, decisionCreated, err := w.store.CommitProvisionResolution(ctx, resolutionRequest)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	return ProvisionResolutionWorkflowResult{
		Observation: observation, Decision: decision, Operation: current,
		DiscoveryCreated: discoveryCreated, DecisionCreated: decisionCreated,
	}, nil
}

// BuildProvisionResolutionRequest creates the provider-neutral operator CAS.
// The exact confirmation value is deliberately not persisted; its digest is
// bound to the immutable observation selected by the operator.
func BuildProvisionResolutionRequest(
	record OperationRecord,
	observation ProvisionDiscoveryObservation,
	operatorSubjectID, idempotencyKey, confirmation string,
) (ProvisionResolutionRequest, error) {
	required := "adopt-provision:" + strings.TrimSpace(observation.OperationID)
	if strings.TrimSpace(confirmation) != required {
		return ProvisionResolutionRequest{}, fmt.Errorf("%w: exact confirmation %q is required", ErrInvalidRequest, required)
	}
	operatorSubjectID = strings.TrimSpace(operatorSubjectID)
	return ProvisionResolutionRequest{
		TenantID: observation.TenantID, OperationID: observation.OperationID,
		ObservationID: observation.ObservationID, ObservationSnapshotDigest: observation.SnapshotDigest,
		ExpectedHeadSequence: record.Head.Sequence, ExpectedHeadReceiptDigest: record.Head.ReceiptDigest,
		ExpectedResolutionRevision: observation.ObservedResolutionRevision,
		IdempotencyKey:             idempotencyKey, OperatorSubjectID: operatorSubjectID,
		OperatorAttestationRef: "provider-attestation://techstack/operator-resolution/" + observation.OperationID,
		OperatorAttestationDigest: prefixedSHA256(
			"providercontrol/operator-resolution/v1",
			[]byte(operatorSubjectID+"\x00"+required+"\x00"+observation.SnapshotDigest),
		),
	}, nil
}

func verifyProvisionResolutionOperatorAttestation(
	observation ProvisionDiscoveryObservation,
	request ProvisionResolutionRequest,
) error {
	required := "adopt-provision:" + observation.OperationID
	expectedRef := "provider-attestation://techstack/operator-resolution/" + observation.OperationID
	expectedDigest := prefixedSHA256(
		"providercontrol/operator-resolution/v1",
		[]byte(request.OperatorSubjectID+"\x00"+required+"\x00"+observation.SnapshotDigest),
	)
	if request.OperatorAttestationRef != expectedRef || request.OperatorAttestationDigest != expectedDigest {
		return ErrProvisionResolutionEvidence
	}
	return nil
}
