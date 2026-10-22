package jobs

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"context"
	"path/filepath"
	"fmt"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
	"crypto/sha256"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"strings"
	"testing"
)

type recordingStackKitCommandSender struct {
	commands    []*agentpb.StackKitCommand
	applyStatus string
}

func (sender *recordingStackKitCommandSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	commandResult := typedStackKitFakeSuccessResult(command)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY && sender.applyStatus != "" {
		commandResult = typedApplyCommandResult(sender.applyStatus)
	}
	return &agentpb.StackKitResult{
		Success:           true,
		CommandResultJson: commandResult,
		Release:           command.Release,
	}, nil
}

func typedApplyCommandResult(status string) []byte {
	receipt, _ := json.Marshal(map[string]interface{}{
		"schemaVersion": "stackkit.command-result/v1", "command": "stackkit apply", "status": "success",
		"data": map[string]interface{}{"schemaVersion": "stackkit.apply-result/v2", "status": status},
	})
	return receipt
}

const testAdvancedTrustBundle = `{"keys":[{"issuerId":"techstack.test","keyId":"ed25519://sha256/` +
	`56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c","publicKey":"A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg"}],` +
	`"schemaVersion":"stackkit.advanced-trust-bundle/v1"}`

// staticAdvancedIssuer serves the test bundle and accepts every binding.
type staticAdvancedIssuer struct{}

func (staticAdvancedIssuer) TrustBundle() []byte { return []byte(testAdvancedTrustBundle) }

func (staticAdvancedIssuer) RecordTrustBinding(context.Context, advancedissuer.TrustBinding) error {
	return nil
}

func (staticAdvancedIssuer) TrustBindingFor(context.Context, string, string) (advancedissuer.TrustBinding, error) {
	return advancedissuer.TrustBinding{StackID: "cloud-stack", OwnerRef: "owner/local/00112233445566778899aabbccddeeff"}, nil
}

func (staticAdvancedIssuer) Issue(_ context.Context, request advancedissuer.Request) (advancedissuer.Capability, error) {
	return advancedissuer.Capability{ID: "capability-1", Operations: request.Operations, Raw: []byte(`{"allowedOperations":["` + strings.Join(request.Operations, `","`) + `"]}`)}, nil
}

// typedStackKitFakeSuccessResult answers like the pinned CLI for the
// operations whose evidence the rollout admits.
func typedStackKitFakeSuccessResult(command *agentpb.StackKitCommand) []byte {
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		return typedPlanCommandResult(testResolvedPlanHash)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT:
		digest := sha256.Sum256(command.AdvancedTrustBundle)
		receipt, _ := json.Marshal(map[string]interface{}{
			"schemaVersion": "stackkit.command-result/v1", "command": "stackkit advanced trust import", "status": "success",
			"data": map[string]interface{}{
				"schemaVersion": "stackkit.local-advanced-trust/v1", "bundleSHA256": "sha256:" + hex.EncodeToString(digest[:]),
				"ownerRef": "owner/local/00112233445566778899aabbccddeeff",
			},
		})
		return receipt
	default:
		return []byte(`{}`)
	}
}

func typedStackKitApplyRequest() StackKitLifecycleRequest {
	return StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
		CandidateSpecJSON:   []byte(`{"apiVersion":"stackkit/v2alpha2","metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"},"workloads":{"cloud-core":{"alternative":"standalone"}},"modules":{"stackkits-cloud-core-runtime":{"computeProfile":"standard"}}}`),
		AdvancedTrustBundle: []byte(testAdvancedTrustBundle),
	}
}

func typedPlanCommandResult(planHash string) []byte {
	inspection, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "stackkit.plan-inspection/v1", "kind": "PlanInspection", "verifiedPhase": "generation",
		"binding":         map[string]interface{}{"planHash": planHash},
		"readiness":       map[string]interface{}{"generation": map[string]interface{}{"status": "ready", "blockers": []string{}}},
		"executorInvoked": false,
	})
	receipt, _ := json.Marshal(map[string]interface{}{
		"schemaVersion": "stackkit.command-result/v1", "command": "stackkit plan", "status": "success",
		"data": map[string]interface{}{"output": string(inspection)},
	})
	return receipt
}

func configureTestStackKitRelease(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "stackkit")
	content := []byte("test stackkit release")
	if err := os.WriteFile(binary, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	pin, err := json.Marshal(stackkitrelease.Pin{
		SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "cloud-kit", Version: "v0.37.1",
		Platform:      stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		ArchiveSHA256: digest, IndexSHA256: digest, BinarySHA256: digest, BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(dir, "pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestAdvancedCatalog(t, dir)
	t.Setenv(stackKitReleasePinEnv, pinPath)
	t.Setenv(stackKitReleaseCacheEnv, dir)
}

// writeTestAdvancedCatalog places the pinned release's Advanced operations
// catalog beside a test release pin, as the image build does.
func writeTestAdvancedCatalog(t *testing.T, dir string) {
	t.Helper()
	catalog, err := os.ReadFile(filepath.Join("..", "..", "internal", "stackkitrelease", "testdata", "advanced-operations-v0.50.0.json")) // #nosec G304 -- fixed repository fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stackkitrelease.AdvancedOperationsFileName), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
}

const testResolvedPlanHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// releaseWithAdvancedCatalog resolves an artifact pin whose release ships the
// stackkit.advanced-operations/v1 catalog beside the pin, as the image does.
func releaseWithAdvancedCatalog(t *testing.T, executable ...[]byte) stackkitrelease.Release {
	return releaseWithAdvancedCatalogVersion(t, "v0.47.0", executable...)
}

func releaseWithAdvancedCatalogVersion(t *testing.T, version string, executable ...[]byte) stackkitrelease.Release {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "stackkit")
	content := []byte("stand-in")
	if len(executable) > 0 {
		content = executable[0]
	}
	if err := os.WriteFile(binary, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	pin, _ := json.Marshal(map[string]any{
		"schemaVersion": stackkitrelease.PinSchemaVersion, "kit": "basement-kit", "version": version,
		"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64),
		"binarySha256": hex.EncodeToString(digest[:]), "binaryPath": binary,
	})
	pinPath := filepath.Join(root, "stackkits-release-pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join("..", "..", "internal", "stackkitrelease", "testdata", "advanced-operations-v0.50.0.json")) // #nosec G304 G703 -- fixed repository fixture.
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(root, stackkitrelease.AdvancedOperationsFileName), catalog, 0o600); writeErr != nil { // #nosec G703 -- test temp directory.
		t.Fatal(writeErr)
	}
	release, err := (stackkitrelease.Cache{Root: root}).ResolvePin(pinPath)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

