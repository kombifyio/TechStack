package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// This explicit development compatibility lane consumes executable artifacts
// and their receipts. Production admission still requires a published release.
func TestApplicationDevelopmentCLICompatibility(t *testing.T) {
	binary := os.Getenv("TECHSTACK_STACKKITS_DEV_ADOPTION_BINARY")
	receiptsOnly := os.Getenv("TECHSTACK_STACKKITS_DEV_ADOPTION_RECEIPTS_ONLY") == "1"
	if binary == "" && !receiptsOnly {
		t.Skip("explicit development CLI compatibility opt-in required")
	}
	releaseVersion := os.Getenv("TECHSTACK_STACKKITS_DEV_ADOPTION_RELEASE_VERSION")
	if releaseVersion == "" {
		releaseVersion = "v0.50.0"
	}
	if receiptsOnly {
		t.Log("retained receipt compatibility; native source execution is disabled")
	}
	workspace := os.Getenv("TECHSTACK_STACKKITS_DEV_ADOPTION_WORKSPACE")
	catalogPath := os.Getenv("TECHSTACK_STACKKITS_DEV_ADOPTION_CATALOG")
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := stackkitrelease.DecodeAdvancedOperations(raw, releaseVersion)
	if err != nil {
		t.Fatal(err)
	}
	inspectRaw, err := os.ReadFile(filepath.Join(workspace, "inspect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Data stackkitcommand.ApplicationEvidence `json:"data"`
	}
	if err = json.Unmarshal(inspectRaw, &fixture); err != nil {
		t.Fatal(err)
	}
	pin := &agentpb.StackKitReleasePin{Version: releaseVersion, PlatformOs: "windows", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}
	cases := []struct {
		file   string
		op     agentpb.StackKitOperation
		action string
	}{
		{"inspect.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT, ""},
		{"adopt.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT, ""},
		{"verify.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY, ""},
		{"control-stop.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL, "stop"},
		{"control-start.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL, "start"},
		{"control-restart.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL, "restart"},
		{"release.json", agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_RELEASE, ""},
	}
	for _, item := range cases {
		t.Run(item.file, func(t *testing.T) {
			stdout, err := os.ReadFile(filepath.Join(workspace, item.file))
			if err != nil {
				t.Fatal(err)
			}
			var signed struct {
				Data struct {
					Result stackkitcommand.ApplicationEvidence `json:"result"`
				} `json:"data"`
			}
			_ = json.Unmarshal(stdout, &signed)
			c := &agentpb.StackKitCommand{CommandId: "compat-" + item.file, Operation: item.op, ServiceKey: "smart-home", StackkitInstanceId: "compat-instance", WorkingDirectory: workspace, Release: pin, ApplicationContainerId: fixture.Data.Source.ContainerID, ApplicationAction: item.action}
			if item.op != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT && item.op != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY {
				c.OwnerApproved = true
				c.ApplicationOperationId = signed.Data.Result.OperationID
			}
			if item.op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT {
				c.ApplicationPlanDigest = fixture.Data.PlanDigest
			}
			if err := stackkitcommand.ValidateCommand(c); err != nil {
				t.Fatal(err)
			}
			argv, err := catalog.RenderArgv(stackkitcommand.ApplicationOperationName(c.Operation), applicationArgValues(c))
			if err != nil {
				t.Fatal(err)
			}
			if item.op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT && !receiptsOnly {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				process := exec.CommandContext(ctx, binary, append([]string{"--chdir", workspace, "--spec", "stack-spec.yaml"}, argv...)...)
				var stderr strings.Builder
				process.Stderr = &stderr
				stdout, err = process.Output()
				if err != nil {
					t.Fatalf("actual CLI source assessment failed: %v; %s", err, stderr.String())
				}
			}
			result := finishStackKitResult(&agentpb.StackKitResult{CommandId: c.CommandId, Release: pin}, c, stdout, nil, nil, time.Now().UTC())
			if err := stackkitcommand.ValidateResult(result, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Opt-in source compatibility only. The adapter must execute the real native
// CLI with synthetic daemon/auth/provider dependencies; retained JSON emitters
// do not qualify this journey and production never consumes this artifact pin.
func TestApplicationConnectorDevelopmentCLICompatibility(t *testing.T) {
	pinPath := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_PIN")
	if pinPath == "" {
		t.Skip("explicit actual CLI development fixture opt-in required")
	}
	workspace := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_WORKSPACE")
	raw, err := os.ReadFile(os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := stackkitcommand.DecodeConnectorRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Dir(pinPath)
	release, err := (stackkitrelease.Cache{Root: cache}).ResolvePin(pinPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_SPEC")
	if spec == "" {
		spec = "stack-spec.yaml"
	}
	id := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_OPERATION_ID")
	instance := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_INSTANCE_ID")
	plan := os.Getenv("TECHSTACK_STACKKITS_DEV_CONNECTOR_PLAN_HASH")
	if id == "" || instance == "" || plan == "" {
		t.Fatal("fixture requires exact original operation, StackSpec instance and signedApply plan")
	}
	executor := &StackKitExecutor{pinPath: pinPath, cacheRoot: cache}
	command := &agentpb.StackKitCommand{CommandId: id + "-dispatch", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR, WorkingDirectory: workspace, SpecPath: spec, ServiceKey: "smart-home", StackkitInstanceId: instance, ApplicationOperationId: id, ApplicationConnectorRequestJson: raw, OwnerApproved: true, ExpectedPlanHash: plan, LocalSiteRef: request.Connector.Binding.Node.SiteRef, LocalNodeRef: request.Connector.Binding.Node.NodeRef, LocalExecutionChannelRef: request.Connector.Binding.Node.ExecutionChannelRef, Release: stackKitReleasePinFromReceipt(release.Receipt()), TimeoutSeconds: 120}
	ensure := executor.Execute(t.Context(), command)
	if err = stackkitcommand.ValidateResult(ensure, command); err != nil || !ensure.Success {
		t.Fatalf("actual native fixture ensure failed: %v; %s", err, ensure.Stderr)
	}
	evidence, _, err := stackkitcommand.ParseApplicationConnectorEvidence(command, ensure)
	if err != nil {
		t.Fatal(err)
	}
	command.CommandId = id + "-read-reconcile"
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY
	command.OwnerApproved = false
	current := executor.Execute(t.Context(), command)
	if err = stackkitcommand.ValidateResult(current, command); err != nil || !current.Success {
		t.Fatalf("actual native fixture original-operation current read failed: %v; %s", err, current.Stderr)
	}
	verified, _, err := stackkitcommand.ParseApplicationConnectorEvidence(command, current)
	if err != nil || verified.ReceiptBase64 != evidence.ReceiptBase64 || verified.ReceiptDigest != evidence.ReceiptDigest {
		t.Fatalf("current read changed original exact signed receipt: %v", err)
	}
	// A request-file is an invocation-private metadata carrier. A failed cleanup
	// is observable because it would leave the original bounded request reusable.
	leftovers, err := filepath.Glob(filepath.Join(workspace, ".stackkit", ".techstack-connector-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range leftovers {
		data, readErr := os.ReadFile(path)
		if readErr == nil && string(data) == string(raw) {
			t.Fatal("invocation-private connector metadata survived execution")
		}
	}
	t.Log("actual native CLI ensure and original-operation read verification passed with synthetic daemon/auth/provider authorities; no production or deployed node qualification")
}
