package orchestrator

import (
	"context"
	"encoding/json"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

// nodeStackKitRelease reads the StackKits runtime a node's Guard last
// reported: the installed release pin and the update it proposes.
func (o *Orchestrator) nodeStackKitRelease(ctx context.Context, tenantID, agentID string) (jobs.NodeStackKitRelease, error) {
	var node jobs.NodeStackKitRelease
	if o.workerStore == nil {
		return node, nil
	}
	worker, err := o.workerStore.GetWorker(ctx, tenantID, agentID)
	if err != nil || worker == nil {
		return node, err
	}
	raw, err := json.Marshal(worker.Resources["runtime_convergence"])
	if err != nil {
		return node, err
	}
	var snapshot runtimeconvergence.Snapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return node, nil
	}
	snapshot, err = runtimeconvergence.Normalize(snapshot)
	if err != nil {
		return node, nil
	}
	for _, component := range snapshot.Components {
		if component.Name != runtimeconvergence.StackKitsRuntimeComponent || component.Release == nil || component.UpdateAvailable == nil {
			continue
		}
		release := component.Release
		node.Installed = &agentpb.StackKitReleasePin{
			Version: release.Version, PlatformOs: release.PlatformOS, PlatformArch: release.PlatformArch,
			ArchiveSha256: release.ArchiveSHA256, ReleaseIndexSha256: release.ReleaseIndexSHA256,
		}
		node.UpdateAvailable = component.UpdateAvailable.Version
	}
	return node, nil
}
