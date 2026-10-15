package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/unifier"
)

type retryProjectionGenerator struct {
	fakeStackKitArtifactGenerator
	document         map[string]interface{}
	bindNodeDocument bool
	addressPrefix    string
}

func (g *retryProjectionGenerator) GenerateStackKitArtifacts(ctx context.Context, req StackKitArtifactGenerateRequest) (*StackKitArtifactGenerateResult, error) {
	raw, err := os.ReadFile(req.StackSpecPath)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &g.document); err != nil {
		return nil, err
	}
	if g.bindNodeDocument {
		g.resultStackSpecPath = filepath.Join(filepath.Dir(req.StackSpecPath), "stack-spec.address-bound.v2.json")
		// Match native intent persistence: a changed candidate cannot replace
		// an existing bound document without its normalized CAS hash.
		if previous, err := os.ReadFile(g.resultStackSpecPath); err == nil {
			var current map[string]interface{}
			if json.Unmarshal(previous, &current) != nil || !reflect.DeepEqual(current, g.document) {
				return nil, errors.New("controller binding requires current intent CAS")
			}
		}
		if !isPathWithinDir(req.OutputDir, req.WorkDir) {
			return nil, errors.New("preparation output escapes renderer workspace")
		}
		if err := os.WriteFile(g.resultStackSpecPath, raw, 0600); err != nil {
			return nil, err
		}
	}
	result, err := g.fakeStackKitArtifactGenerator.GenerateStackKitArtifacts(ctx, req)
	if err == nil && g.addressPrefix != "" {
		result.Metadata[metadataKeyKombifyMeAddressPrefix] = g.addressPrefix
	}
	return result, err
}

type retryCASCommander struct {
	*typedLifecycleCommander
	currentSpecHash    string
	inspectionSpecHash string
}

func (s *retryCASCommander) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if strings.HasSuffix(command.CommandId, "-current-intent-plan") {
		s.commands = append(s.commands, command)
		if command.SpecPath != "stack-spec.address-bound.v2.json" || len(command.InventoryJson) != 0 {
			return nil, errors.New("current intent inspection selected another document or replaced inventory")
		}
		var receipt map[string]interface{}
		_ = json.Unmarshal(typedPlanCommandResult(s.planHash), &receipt)
		data := mapFromInterface(receipt["data"])
		var inspection map[string]interface{}
		_ = json.Unmarshal([]byte(stringFromMap(data, "output")), &inspection)
		mapFromInterface(inspection["binding"])["specHash"] = s.inspectionSpecHash
		raw, _ := json.Marshal(inspection)
		data["output"] = string(raw)
		raw, _ = json.Marshal(receipt)
		return &agentpb.StackKitResult{Success: true, CommandResultJson: raw, Release: command.Release}, nil
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
		if command.ExpectedSpecHash != s.currentSpecHash || command.SpecPath != "stack-spec.address-bound.v2.json" {
			return nil, errors.New("existing native intent requires its normalized CAS hash")
		}
	}
	return s.typedLifecycleCommander.SendStackKitCommand(ctx, agent, command)
}

// An admitted repair's callback route must reach the renderer and node INIT,
// even when the original preparation left a complete but older native handoff.
// Altered or unbound snapshots must never reach either execution boundary.
func TestDeployHandlerExactRetryPreservesApprovedNativeRoutes(t *testing.T) {
	for _, scenario := range []string{"admitted", "current intent", "partial INIT CAS", "partial wrong CAS", "partial malformed CAS", "prefix-bound current intent", "prefix-bound partial INIT", "missing current hash", "changed runtime binding", "missing digest", "altered snapshot", "missing snapshot", "altered persisted snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			stackID := "stack-repaired-routes"
			baseDir := t.TempDir()
			persistDeployFixture(t, baseDir, stackID)
			persister, err := unifier.NewSpecPersisterWithPath(filepath.Join(baseDir, stackID))
			if err != nil {
				t.Fatal(err)
			}
			stale := map[string]interface{}{
				"apiVersion": "stackkit/v2alpha2", "kind": "StackSpec",
				"metadata":   map[string]interface{}{"name": "owned"},
				"kit":        map[string]interface{}{"slug": "basement-kit"},
				"generation": map[string]interface{}{"target": "compose", "outputRoot": "deploy"},
				"network":    map[string]interface{}{"domain": map[string]interface{}{"base": "owned.example"}},
				"workloads":  map[string]interface{}{"vault": map[string]interface{}{"enabled": true}},
			}
			cached, _ := json.Marshal(stale)
			if _, _, err := persister.SaveStackSpecBytes(cached); err != nil {
				t.Fatal(err)
			}
			parentBound := filepath.Join(persister.BaseDir, "stack-spec.address-bound.v2.json")
			if err := os.WriteFile(parentBound, cached, 0600); err != nil {
				t.Fatal(err)
			}
			candidate := map[string]interface{}{}
			if err := json.Unmarshal(cached, &candidate); err != nil {
				t.Fatal(err)
			}
			routes := map[string]interface{}{"vault": map[string]interface{}{
				"moduleRef": "stackkits-vaultwarden-runtime", "serviceRef": "vault",
				"accessPolicyRef": "vault-policy", "host": "vault.owned.example", "targetPort": float64(80),
			}}
			candidate["routes"] = routes
			candidate["access"] = map[string]interface{}{"vault-policy": map[string]interface{}{"mode": "identity"}}
			raw, _ := json.Marshal(candidate)
			job := &Job{ID: "deploy-repaired-routes", Type: JobTypeDeploy, TargetID: stackID, Payload: map[string]interface{}{
				"workers": deployWorkerPayload(), "tenant_id": "tenant-1", "owner_id": "owner-1",
				"rollout_retry_kind": "exact-existing-runtime", "rollout_retry_source_job_id": "failed-initial",
				"rollout_retry_key": "exact-recovery", "rollout_retry_lease_id": "lease-1",
				"rollout_retry_server_id": "server-1", "rollout_retry_replacement_job_id": "deploy-repaired-routes",
				payloadKeyStackSpecV2: candidate, RolloutRetryCandidateSHA256Field: retryProjectionTestDigest(raw),
			}}
			switch scenario {
			case "missing digest":
				delete(job.Payload, RolloutRetryCandidateSHA256Field)
			case "altered snapshot":
				candidate["routes"] = map[string]interface{}{}
			case "missing snapshot":
				delete(job.Payload, payloadKeyStackSpecV2)
			case "altered persisted snapshot":
				path, err := persister.SaveStackSpecSnapshotBytes(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, cached, 0600); err != nil {
					t.Fatal(err)
				}
			}
			generator := &retryProjectionGenerator{}
			prefixBound := strings.HasPrefix(scenario, "prefix-bound ")
			partialInit := strings.HasPrefix(scenario, "partial ") || scenario == "prefix-bound partial INIT"
			currentIntent := scenario == "current intent" || scenario == "missing current hash" || scenario == "changed runtime binding" || partialInit || prefixBound
			if currentIntent {
				generator.bindNodeDocument = true
				if prefixBound {
					// The default kombify.me rollout: INIT keeps the node's init
					// file and ADDRESS_BIND persists the bound document beside it.
					generator.addressPrefix = "owned-a1b2"
				}
				job.Payload["lease_id"] = "lease-1"
				job.Payload["rollout_retry_server_id"] = runtimeidentity.LeaseServerID("lease-1")
				job.Payload[RolloutRetryCurrentIntentField] = map[string]interface{}{
					"source_job_id": "failed-initial", "lease_id": "lease-1", "server_id": runtimeidentity.LeaseServerID("lease-1"),
					"agent_id": runtimeidentity.LeaseRuntimeAgentID("tenant-1", "lease-1"), "stackkit": "basement-kit",
					"spec_path": "/data/stacks/stack-repaired-routes/stack-spec.address-bound.v2.json", "release": "v0.37.1",
				}
				if scenario == "changed runtime binding" {
					mapFromInterface(job.Payload[RolloutRetryCurrentIntentField])["agent_id"] = "foreign-worker"
				}
				if partialInit {
					mapFromInterface(job.Payload[RolloutRetryCurrentIntentField])["apply_source_job_id"] = "prior-apply"
					job.Payload[RolloutRetryCurrentSpecHashField] = "sha256:" + strings.Repeat("b", 64)
					if scenario == "partial wrong CAS" {
						job.Payload[RolloutRetryCurrentSpecHashField] = "sha256:" + strings.Repeat("c", 64)
					}
					if scenario == "partial malformed CAS" {
						job.Payload[RolloutRetryCurrentSpecHashField] = "raw-file-hash"
					}
				}
			}
			cfg := &ProvisionConfig{WorkDir: t.TempDir(), SpecBaseDir: baseDir, RuntimeActions: RuntimeActions{
				StackKitGenerator: generator,
				SimulationGate:    &fakeRuntimeRunner{result: map[string]interface{}{"status": "accepted"}},
			}}
			configureTypedLifecycle(t, cfg, nil)
			commander := cfg.StackKitCommander.(*typedLifecycleCommander)
			if currentIntent && !prefixBound {
				currentHash := "sha256:" + strings.Repeat("b", 64)
				cas := &retryCASCommander{typedLifecycleCommander: commander, currentSpecHash: currentHash, inspectionSpecHash: currentHash}
				if scenario == "missing current hash" {
					cas.inspectionSpecHash = ""
				}
				cfg.StackKitCommander = cas
			}
			err = DeployHandler(cfg)(t.Context(), job, &Queue{jobs: map[string]*Job{job.ID: job}})
			if scenario == "missing current hash" || scenario == "changed runtime binding" || scenario == "partial wrong CAS" || scenario == "partial malformed CAS" || scenario == "prefix-bound partial INIT" {
				if err == nil {
					t.Fatal("unbound current intent reached replacement")
				}
				for _, command := range commander.commands {
					if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY || (scenario != "partial wrong CAS" && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT) {
						t.Fatal("unverified current intent reached a mutating command")
					}
				}
				return
			}
			if scenario != "admitted" && scenario != "current intent" && scenario != "partial INIT CAS" && scenario != "prefix-bound current intent" {
				if err == nil || generator.document != nil || len(commander.commands) != 0 {
					t.Fatalf("unbound repair reached execution: err=%v rendered=%v dispatched=%v", err, generator.document, commander.commands)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertRetryRouteEffect(t, generator.document, routes)
			var initialized map[string]interface{}
			for _, command := range commander.commands {
				if partialInit && (strings.HasSuffix(command.CommandId, "-current-intent-plan") || (command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_INIT && command.ExpectedSpecHash != "")) {
					t.Fatal("caller CAS precondition affected an operation other than canonical INIT")
				}
				if prefixBound && (strings.HasSuffix(command.CommandId, "-current-intent-plan") || command.ExpectedSpecHash != "") {
					t.Fatal("a bound generation hash authorized replacement of the node's init file")
				}
				if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
					if err := json.Unmarshal(command.CandidateSpecJson, &initialized); err != nil {
						t.Fatal(err)
					}
				}
			}
			assertRetryRouteEffect(t, initialized, routes)
			retained, err := os.ReadFile(persister.GetStackSpecPath())
			if err != nil || string(retained) != string(cached) {
				t.Fatalf("retry changed another rollout's native handoff: %v", err)
			}
			retained, err = os.ReadFile(parentBound)
			if err != nil || string(retained) != string(cached) {
				t.Fatalf("retry changed an earlier controller's bound intent: %v", err)
			}
		})
	}
}

func retryProjectionTestDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func assertRetryRouteEffect(t *testing.T, document, routes map[string]interface{}) {
	t.Helper()
	if !reflect.DeepEqual(document["routes"], routes) || stringFromMap(mapFromInterface(document["generation"]), "target") != "terramate" {
		t.Fatalf("managed execution lost the approved authenticated route or target: %v", document)
	}
}
