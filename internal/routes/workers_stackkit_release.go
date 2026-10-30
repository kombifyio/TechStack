package routes

import "github.com/kombifyio/techstack/pkg/httpx"

// The retained compiler runtime uses this configuration independently of edge
// delivery. No request handler opens or streams this local artifact.
const stackKitReleaseBundleEnv = "TECHSTACK_STACKKIT_RELEASE_BUNDLE"

func (h workerRouteHandlers) agentStackKitRelease(e *httpx.Event) error {
	return h.admitArtifact(e, "stackkit_runtime")
}
