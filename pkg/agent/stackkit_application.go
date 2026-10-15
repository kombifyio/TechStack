package agent

import (
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func (executor *StackKitExecutor) ApplicationCapabilities() []string {
	if !executor.Available() {
		return nil
	}
	release, err := (stackkitrelease.Cache{Root: executor.cacheRoot}).ResolvePin(executor.pinPath)
	if err != nil {
		return nil
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil
	}
	var capabilities []string
	adoption := true
	for _, op := range []string{"application.inspect", "application.adopt", "application.verify", "application.release", "application.control"} {
		if catalog.Dispatchable(op) != nil {
			adoption = false
		}
	}
	if adoption {
		capabilities = append(capabilities, stackkitcommand.ApplicationAdoptionCapability)
	}
	if catalog.Dispatchable("application.connector") == nil && catalog.Dispatchable("application.connector.verify") == nil {
		capabilities = append(capabilities, stackkitcommand.ApplicationConnectorCapability)
	}
	return capabilities
}
func stackKitApplicationArgs(release stackkitrelease.Release, c *agentpb.StackKitCommand) ([]string, error) {
	if err := stackkitcommand.ValidateCommand(c); err != nil {
		return nil, err
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, err
	}
	return catalog.RenderArgv(stackkitcommand.ApplicationOperationName(c.Operation), applicationArgValues(c))
}

func applicationArgValues(c *agentpb.StackKitCommand) map[string]string {
	return map[string]string{"workloadRef": c.ServiceKey, "containerId": c.ApplicationContainerId, "planDigest": c.ApplicationPlanDigest, "operationId": c.ApplicationOperationId, "applicationAction": c.ApplicationAction}
}
