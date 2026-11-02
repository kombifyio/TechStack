package routes

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// withheldStackKitsRuntime reports whether Techstack keeps a deployed node on
// its installed StackKits runtime. A newer CLI refuses every plan an older one
// compiled, so a deployed node receives the published release only as the
// target of a succeeded owner-approved governed upgrade; until then the
// runtime endpoint answers 304 for the installed bundle and the Guard reports
// the published release as available. A security patch takes the same path:
// Techstack starts its governed upgrade without approval (pkg/stackkitsupdate)
// and never serves a runtime under a plan the installed CLI compiled. A node
// without a deployment or without an installed runtime receives the published
// release.
func (h workerRouteHandlers) withheldStackKitsRuntime(e *httpx.Event, published, installedDigest, publishedDigest string) bool {
	if installedDigest == "" || installedDigest == publishedDigest || h.wst == nil {
		return false
	}
	agentID := strings.TrimSpace(e.Request.Header.Get(agentBinaryRuntimeAgentIDHeader))
	tenantID := runtimeAgentTenantIDFromRequest(e)
	if agentID == "" || tenantID == "" {
		return false
	}
	worker, err := h.wst.GetWorker(e.Request.Context(), tenantID, agentID)
	if err != nil {
		return true
	}
	if worker == nil || strings.TrimSpace(worker.StackID) == "" {
		return false
	}
	target, ok := controlplane.WorkerStackKitsTargetOf(worker)
	return !ok || target.Outcome != controlplane.StackKitsTargetSucceeded || stackKitsReleaseTag(target.Version) != stackKitsReleaseTag(published)
}

func stackKitsReleaseTag(version string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}
