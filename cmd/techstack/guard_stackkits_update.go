package main

import (
	"context"
	"log/slog"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

// approvedStackKitsUpgrade reports whether a command was the owner-approved
// governed `stackkit upgrade` that compiled the node's plan with a new release.
func approvedStackKitsUpgrade(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) bool {
	return command.GetOperation() == agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE &&
		command.GetOwnerApproved() && !command.GetDryRun() && result.GetSuccess()
}

// publishStackKitsRuntimeAfterUpgrade converges to the release Techstack
// serves once it recorded the approved upgrade's success, and reports the new
// installed release at once: until a heartbeat names it, the control plane
// still addresses the replaced release. Techstack records the target only
// after this command's result reached it, so the install is retried briefly.
func publishStackKitsRuntimeAfterUpgrade(ctx context.Context, runtime runtimePackageConverger, cfg agentpkg.StackKitRuntimeBootstrapConfig, tracker *runtimeconvergence.Tracker, log *slog.Logger, before string) {
	for attempt := 0; attempt < stackKitsUpgradeConvergenceAttempts; attempt++ {
		timer := time.NewTimer(stackKitsUpgradeConvergenceDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		result, err := runtime.EnsureRuntimeEvidence(ctx, cfg)
		if err != nil || result.Version == before {
			continue
		}
		now := time.Now().UTC()
		current := tracker.Snapshot()
		components := make([]runtimeconvergence.Component, 0, len(current.Components))
		for _, component := range current.Components {
			if component.Name == runtimeconvergence.StackKitsRuntimeComponent {
				component = runtimeconvergence.Component{
					Name: component.Name, State: runtimeconvergence.ComponentReady, ObservedAt: now,
					Version: result.Version, ArtifactSHA256: result.SHA256,
					Release: stackKitInstalledRelease(result, nil), UpdateAvailable: stackKitUpdateAvailable(result, nil),
				}
			}
			components = append(components, component)
		}
		tracker.Set(runtimeconvergence.Aggregate(now, components...))
		log.Info("stackkit_runtime_after_upgrade", "version", result.Version)
		return
	}
	log.Warn("stackkit_runtime_after_upgrade_pending", "installed", before)
}

const (
	stackKitsUpgradeConvergenceAttempts = 12
	stackKitsUpgradeConvergenceDelay    = 10 * time.Second
)
