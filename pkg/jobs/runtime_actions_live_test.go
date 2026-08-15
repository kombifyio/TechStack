package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
)

func TestLiveRuntimeActionEndpoints(t *testing.T) {
	if os.Getenv("TECHSTACK_RUNTIME_ACTION_LIVE_GATE") != "1" {
		t.Skip("set TECHSTACK_RUNTIME_ACTION_LIVE_GATE=1 to run against real Simulate and StackKits runtime-action endpoints")
	}
	secret := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))
	simulateURL := firstRuntimeActionEnv("TECHSTACK_SIMULATE_ACTIONS_URL", "TECHSTACK_KOMBISIM_URL")
	stackKitsURL := firstRuntimeActionEnv("TECHSTACK_STACKKITS_ACTIONS_URL", "TECHSTACK_STACKKITS_INTERNAL_URL")
	if secret == "" || simulateURL == "" || stackKitsURL == "" {
		t.Fatal("SERVICE_AUTH_SECRET, TECHSTACK_SIMULATE_ACTIONS_URL, and TECHSTACK_STACKKITS_ACTIONS_URL are required")
	}

	workDir := t.TempDir()
	tofuDir := filepath.Join(workDir, "tofu")
	if err := os.MkdirAll(tofuDir, 0750); err != nil {
		t.Fatalf("create tofu dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tofuDir, "main.tf"), []byte("terraform {}\n"), 0600); err != nil {
		t.Fatalf("write tofu fixture: %v", err)
	}
	unifiedPath := filepath.Join(workDir, "unified-spec.yaml")
	if err := os.WriteFile(unifiedPath, []byte("stack_id: live-runtime-gate\nstackkit: basement-kit\n"), 0600); err != nil {
		t.Fatalf("write unified fixture: %v", err)
	}

	req := RuntimeActionRequest{
		StackID:     "live-runtime-gate",
		StackName:   "Live Runtime Gate",
		StackKit:    DefaultBasementKitRef,
		TofuDir:     tofuDir,
		UnifiedPath: unifiedPath,
	}
	actions := []struct {
		name         string
		baseURL      string
		target       string
		action       runtimeaction.Action
		path         string
		rejectStatus string
	}{
		{"simulation", simulateURL, runtimeaction.TargetSimulate, runtimeaction.ActionSimulateUpdate, defaultSimulationGatePath, string(runtimeaction.StatusFailed)},
		{"rollout", stackKitsURL, runtimeaction.TargetStackKits, runtimeaction.ActionStackKitRollout, defaultStackKitsRolloutPath, string(runtimeaction.StatusFailed)},
		{"verification", stackKitsURL, runtimeaction.TargetStackKits, runtimeaction.ActionVerifyRollout, defaultStackKitsVerifyPath, string(runtimeaction.StatusFailed)},
		{"restore", stackKitsURL, runtimeaction.TargetStackKits, runtimeaction.ActionRestoreDrill, defaultRestoreDrillPath, string(runtimeaction.StatusSkipped)},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
				BaseURL:           tc.baseURL,
				Target:            tc.target,
				Action:            string(tc.action),
				Path:              tc.path,
				ServiceAuthSecret: secret,
				ServiceAuthNext:   strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET_NEXT")),
			})
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			result, err := runner.RunWithResult(context.Background(), req)
			if err != nil {
				t.Fatalf("runtime action %s failed: %v", tc.action, err)
			}
			status := resultString(result, "status")
			if status == "" {
				t.Fatalf("runtime action %s returned no status: %#v", tc.action, result)
			}
			if status == tc.rejectStatus {
				t.Fatalf("runtime action %s status = %s, result=%#v", tc.action, status, result)
			}
		})
	}
}
