package homeassistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/runtimeexecutor"
	"github.com/kombifyio/techstack/internal/managedstackkit"
)

func TestSealedOwnerRejectsTargetAndOriginSubstitution(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/config" {
			t.Error("unexpected native mutation")
			w.WriteHeader(400)
			return
		}
		reads++
		_, _ = w.Write([]byte(`{"version":"2026.9.1"}`))
	}))
	defer server.Close()
	core, err := NewLocalClient(context.Background(), server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	sealed := sealedExistingOwnerRequest(t)
	content := sealed.Artifacts[0].Content
	binding := OwnerBinding{TenantID: "tenant", StackID: "stack", RuntimeAgentID: "agent", Target: sealed.RuntimeTargets[0], Health: sealed.HealthTargets, Instance: &InstanceBinding{BindingRef: "binding", Origin: "existing", ManagementScope: "observed", Core: core}}
	owner := &OwnerOperations{Bindings: []OwnerBinding{binding}}
	input := managedstackkit.OperationsRequest{TenantID: "tenant", StackID: "stack", RuntimeAgentID: "agent", Request: sealed}
	if _, err = owner.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	before := reads
	input.TenantID = "other"
	if _, err = owner.Execute(context.Background(), input); err == nil || reads != before {
		t.Fatal("cross-tenant target was observed")
	}
	input.TenantID = "tenant"
	changed := runtimeexecutor.CloneExecutionRequest(sealed)
	changed.Artifacts[0].Content = []byte(strings.ReplaceAll(string(content), `"existing"`, `"new"`))
	sum := sha256.Sum256(changed.Artifacts[0].Content)
	changed.Artifacts[0].Digest = "sha256:" + hex.EncodeToString(sum[:])
	input.Request, err = runtimeexecutor.SealRequest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Execute(context.Background(), input); err == nil || reads != before {
		t.Fatal("resealed origin substitution gained authority")
	}
}

func sealedExistingOwnerRequest(t *testing.T) runtimeexecutor.ExecutionRequest {
	t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	module := "stackkits-home-assistant-existing-runtime"
	target := runtimeexecutor.RuntimeTarget{RequirementID: "ha", OwnerKind: "module", OwnerRef: module, OwnerContractHash: d, ProviderRef: "stackkits-home-assistant-appliance", ProviderContractHash: d, ModuleRef: module, ModuleContractHash: d, UnitRef: "instance", UnitContractHash: d, RuntimeKind: "external", RuntimeDelivery: "external-control-plane", RuntimeEngine: "api", InstanceRef: "ha-instance", ExecutionChannelRef: "ha-channel", SiteRefs: []string{"home"}, NodeRefs: []string{"node-a"}, WorkloadRef: "smart-home", ArtifactRefs: []string{"instance-artifact"}}
	content := []byte(`{"apiVersion":"stackkit.home-assistant-instance/v1","moduleRef":"stackkits-home-assistant-existing-runtime","siteRef":"home","nodeRef":"node-a","instance":{"platform":"home-assistant","installationMethod":"unknown","instanceOrigin":"existing","managementScope":"observed","configurationPolicy":"preserve-user-changes","dataCustody":"external-instance-owner","baselinePolicy":"preserve"}}`)
	sum := sha256.Sum256(content)
	raw := runtimeexecutor.ExecutionRequest{Executor: runtimeexecutor.ExecutorIdentity{ID: "ha-owner", Version: "1.0.0", Digest: d}, PlanHash: d, ManifestHash: d, GenerationReceiptHash: d, RequirementsHash: d, EvidenceBundleHash: d, RuntimeTargets: []runtimeexecutor.RuntimeTarget{target}, Artifacts: []runtimeexecutor.Artifact{{ID: "instance-artifact", Kind: "native-config", Format: "json", Mode: "0640", OwnerKind: "render-instance", OwnerRef: target.InstanceRef, OwnerContractHash: d, ProviderRef: target.ProviderRef, ProviderContractHash: d, ModuleRef: module, ModuleContractHash: d, UnitRef: "instance", UnitContractHash: d, InstanceRef: target.InstanceRef, OutputRef: "workloads/home-assistant-existing/instance.json", SiteRefs: target.SiteRefs, NodeRefs: target.NodeRefs, Content: content, Digest: "sha256:" + hex.EncodeToString(sum[:])}}}
	sealed, err := runtimeexecutor.SealRequest(raw)
	if err != nil {
		t.Fatal(err)
	}

	return sealed
}
