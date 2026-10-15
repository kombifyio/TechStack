package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The authenticated PLAN seam cannot export unrelated files/credential fields
// or accept a plan from another instance/generation.
func TestManagedPlanExportsOnlyInspectedBackupAuthority(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, "deploy", ".stackkit")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	inspection := `{"apiVersion":"stackkit.plan-inspection/v1","kind":"PlanInspection","verifiedPhase":"generation","binding":{"planHash":"` + hash + `"},"readiness":{"generation":{"status":"ready"}}}`
	plan := map[string]any{"apiVersion": "stackkit.resolved-plan/v1", "kind": "ResolvedPlan", "stackId": "home", "planHash": hash, "backupPolicy": map[string]any{"coverage": "config"}, "credentials": "private-marker"}
	for _, scenario := range []string{"matched", "historical", "source-plan-substitution", "other-instance", "other-generation", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			plan["stackId"] = "home"
			plan["planHash"] = hash
			if scenario == "other-instance" {
				plan["stackId"] = "other"
			}
			if scenario == "other-generation" {
				plan["planHash"] = "sha256:" + strings.Repeat("b", 64)
			}
			path := filepath.Join(directory, "resolved-plan.json")
			raw, _ := json.Marshal(plan)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "symlink" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "private"), path); err != nil {
					t.Fatal(err)
				}
			}
			output := inspection
			if scenario == "historical" || scenario == "source-plan-substitution" {
				var inspected map[string]any
				_ = json.Unmarshal([]byte(inspection), &inspected)
				binding := inspected["binding"].(map[string]any)
				binding["compilerVersion"] = "stackkits-resolver/0.50.6"
				binding["renderer"] = map[string]any{"version": "0.50.6"}
				if scenario == "source-plan-substitution" {
					binding["planHash"] = "sha256:" + strings.Repeat("b", 64)
				}
				source, _ := json.Marshal(map[string]any{
					"apiVersion": "stackkit.source-generation-inspection/v1", "inspection": inspected,
					"sourceRelease":   map[string]any{"schemaVersion": "stackkit.release-receipt/v1", "kit": "cloud-kit", "version": "v0.50.6", "channel": "stable", "archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64)},
					"owner":           map[string]any{"ownerRef": "owner-1", "keyId": "key-1", "pocketIdSubject": "subject-1", "ownerBindingDigest": "sha256:" + strings.Repeat("c", 64)},
					"applyResultHash": "sha256:" + strings.Repeat("d", 64), "evidenceBundleHash": "sha256:" + strings.Repeat("e", 64),
				})
				output = string(source)
			}
			envelope, _ := json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": "stackkit plan", "status": "success", "data": map[string]any{"output": output}})
			result := &agentpb.StackKitResult{Success: true, CommandResultJson: envelope, Release: &agentpb.StackKitReleasePin{Version: "v0.51.3"}}
			if scenario == "historical" || scenario == "source-plan-substitution" {
				if err := attachSourceGenerationInspection(result); err != nil {
					t.Fatal(err)
				}
			}
			err := attachManagedBackupPlan(workspace, "home", result)
			if scenario != "matched" && scenario != "historical" {
				if err == nil {
					t.Fatal("uninspected plan authority was exported")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			projection, gotHash, err := stackkitcommand.ManagedBackupPlan(result, "home")
			if scenario == "historical" {
				projection, gotHash, err = stackkitcommand.ManagedSourceGenerationBackupPlan(result, "home")
			}
			if err != nil || gotHash != hash || strings.Contains(string(projection), "private-marker") {
				t.Fatal("plan projection lost its binding or disclosed unrelated data")
			}
		})
	}
}
