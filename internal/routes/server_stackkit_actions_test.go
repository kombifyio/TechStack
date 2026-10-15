package routes

import (
	"slices"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The projection must deny Day-2 mutations when any admitted node fact is lost.
// Inputs are pure domain observations; no database or execution fixture is used.
func TestStackKitDay2ActionsRequireBoundConvergedCapabilities(t *testing.T) {
	receipt := stackkitrelease.Receipt{Version: "v0.52.6", ArchiveSHA256: "ef5c23afb267e9ea73ea2c5854ed7ea9a43ba6b69f25e71faf2a146c66f3beb6"}
	catalog := stackkitrelease.AdvancedOperationsCatalog{ReleaseVersion: receipt.Version, Operations: []stackkitrelease.AdvancedOperation{
		{Operation: "terramate.change-set.create", Status: "available", Argv: []string{"advanced", "change-set", "create"}},
		{Operation: "terramate.change-set.apply", Status: "available", Argv: []string{"advanced", "change-set", "apply"}},
		{Operation: "rollback.coordinated", Status: "available", Argv: []string{"advanced", "rollback"}},
		{Operation: "restore.drill", Status: "available", Argv: []string{"advanced", "restore-drill"}},
	}}
	baseCaps := []string{"stackkit", stackkitcommand.ExpectedPlanHashCapability, stackkitcommand.WorkspaceInstanceCapability, stackkitcommand.AdvancedOperationsCapability}
	for _, scenario := range []string{"ready", "legacy identity", "wrong archive", "wrong version", "pending runtime", "missing instance", "missing advanced", "managed backup custody", "partial change set"} {
		t.Run(scenario, func(t *testing.T) {
			caps, err := normalizeObservedStackKitCapabilities(baseCaps)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := runtimeconvergence.Snapshot{State: runtimeconvergence.StateReady, Components: []runtimeconvergence.Component{{Name: runtimeconvergence.StackKitsRuntimeComponent, State: runtimeconvergence.ComponentReady, Version: receipt.Version, ArtifactSHA256: receipt.ArchiveSHA256}}}
			item := serverRuntimeResponse{StackActions: []string{serverStackActionApply, serverStackActionBackupStatus}, Capabilities: &serverRuntimeCapabilities{IdentityBinding: serverIdentityKeyBound, runtimeConvergence: &snapshot}}
			currentCatalog := catalog
			allowBackup, allowAdvanced, allowChange := true, true, true
			switch scenario {
			case "legacy identity":
				item.Capabilities.IdentityBinding = serverIdentityLegacyBearer
				allowBackup, allowAdvanced, allowChange = false, false, false
			case "wrong archive":
				snapshot.Components[0].ArtifactSHA256 = "another artifact"
				allowBackup, allowAdvanced, allowChange = false, false, false
			case "wrong version":
				snapshot.Components[0].Version = "v0.47.7"
				allowBackup, allowAdvanced, allowChange = false, false, false
			case "pending runtime":
				snapshot.State = runtimeconvergence.StatePending
				allowBackup, allowAdvanced, allowChange = false, false, false
			case "missing instance":
				caps = slices.DeleteFunc(caps, func(capability string) bool { return capability == stackkitcommand.WorkspaceInstanceCapability })
				allowBackup, allowAdvanced, allowChange = false, false, false
			case "missing advanced":
				caps = slices.DeleteFunc(caps, func(capability string) bool { return capability == stackkitcommand.AdvancedOperationsCapability })
				allowAdvanced, allowChange = false, false
			case "managed backup custody":
				item.StackActions = []string{serverStackActionApply}
				allowBackup = false
			case "partial change set":
				currentCatalog.Operations = slices.DeleteFunc(slices.Clone(catalog.Operations), func(operation stackkitrelease.AdvancedOperation) bool {
					return operation.Operation == "terramate.change-set.apply"
				})
				allowChange = false
			}
			actions := stackKitDay2Actions(&item, caps, receipt, currentCatalog)
			for action, allowed := range map[string]bool{serverStackActionBackupRun: allowBackup, serverStackActionBackupConfigure: allowBackup, serverStackActionRollback: allowAdvanced, serverStackActionRestoreDrill: allowAdvanced, serverStackActionAdvancedChange: allowChange} {
				if slices.Contains(actions, action) != allowed {
					t.Fatalf("operation %s admitted=%t, want %t", action, slices.Contains(actions, action), allowed)
				}
			}
		})
	}
}
