package providercontrol

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// ProvisionDiscoveryEvidenceTime returns the one timestamp sealed into every
// provider resource evidence document. Empty candidate sets use the explicit
// observation time because there is no resource-level evidence document.
func ProvisionDiscoveryEvidenceTime(observation ProvisionDiscoveryObservation) (time.Time, error) {
	if len(observation.Candidates) == 0 {
		if observation.CollectedAt.IsZero() {
			return time.Time{}, fmt.Errorf("%w: discovery has no collection time", ErrProvisionResolutionEvidence)
		}
		return observation.CollectedAt.UTC(), nil
	}
	var evidenceTime time.Time
	for _, candidate := range observation.Candidates {
		for _, resource := range candidate.Resources {
			for _, evidence := range resource.Evidence {
				current := evidence.CollectedAt.UTC()
				if current.IsZero() {
					return time.Time{}, fmt.Errorf("%w: discovery evidence has no collection time", ErrProvisionResolutionEvidence)
				}
				if evidenceTime.IsZero() {
					evidenceTime = current
				} else if !evidenceTime.Equal(current) {
					return time.Time{}, fmt.Errorf("%w: discovery evidence collection times differ", ErrProvisionResolutionEvidence)
				}
			}
		}
	}
	if evidenceTime.IsZero() {
		return time.Time{}, fmt.Errorf("%w: discovery evidence has no collection time", ErrProvisionResolutionEvidence)
	}
	delta := observation.CollectedAt.UTC().Sub(evidenceTime)
	if delta < 0 {
		delta = -delta
	}
	if delta >= time.Microsecond {
		return time.Time{}, fmt.Errorf("%w: discovery evidence time does not match its observation", ErrProvisionResolutionEvidence)
	}
	return evidenceTime, nil
}

// SameProvisionCandidateResources compares provider reads to the canonical
// receipt form rather than raw adapter values. Candidate order is irrelevant;
// every exact sealed graph must occur once on both sides.
func SameProvisionCandidateResources(
	ctx context.Context,
	record OperationRecord,
	expected, observed []ProvisionCandidateGraph,
	collectedAt time.Time,
) bool {
	if len(expected) != len(observed) {
		return false
	}
	unmatched := make([][]providerexecutor.ResourceBinding, len(observed))
	for index := range observed {
		unmatched[index] = observed[index].Resources
	}
	for _, candidate := range expected {
		sealed, err := providerexecutor.AssembleReceipt(ctx, providerexecutor.ExecutionRequest{
			Command: record.Command, Previous: record.Head,
		}, providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
			Resources: candidate.Resources,
		}, collectedAt, nil)
		if err != nil {
			return false
		}
		match := -1
		for index, resources := range unmatched {
			if resources != nil && reflect.DeepEqual(sealed.Resources, resources) {
				match = index
				break
			}
		}
		if match < 0 {
			return false
		}
		unmatched[match] = nil
	}
	return true
}
