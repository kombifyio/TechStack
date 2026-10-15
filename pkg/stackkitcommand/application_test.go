package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"strings"
	"testing"
	"time"
)

func TestApplicationReceiptRejectsChangedSourceAndUnauthenticatedCustody(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT, ApplicationContainerId: id, ApplicationPlanDigest: digest, ApplicationOperationId: "operation-1"}
	evidence := ApplicationEvidence{APIVersion: "stackkit.application-adoption-result/v1", Authority: ApplicationAuthority{PlanHash: digest, LifecycleContractHash: digest, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", ProfileRef: HomeAssistantAdoptionProfile, Mode: "adopt_in_place", Source: ApplicationSource{ContainerID: id, ImageID: digest, ImageDigest: digest, Revision: digest, ConfigMount: "/srv/ha", ConfigurationDigest: digest, AccountsDigest: digest}, PlanDigest: digest, OperationID: "operation-1", OwnedFields: []string{"container.power-state"}, PreservedFields: []string{"accounts", "configuration", "persistentData"}, AdmittedOperations: []string{"verify", "start", "stop", "restart", "release"}, VerifiedOwnerRef: "ha-owner", Status: "verified", VerifiedAt: time.Now().UTC()}
	resultFor := func(e ApplicationEvidence) *agentpb.StackKitResult {
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": e, "signature": map[string]any{"ownerRef": "local-owner", "keyId": "key-1", "value": "signed"}}})
		hash := sha256.Sum256(raw)
		return &agentpb.StackKitResult{Success: true, CommandResultJson: raw, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(hash[:])}
	}
	valid := resultFor(evidence)
	if _, err := ParseApplicationEvidence(command, valid); err != nil {
		t.Fatal(err)
	}
	valid.PinnedCliEvidenceVerified = false
	if _, err := ParseApplicationEvidence(command, valid); err == nil {
		t.Fatal("unattested receipt admitted")
	}
	changed := evidence
	changed.Source.ContainerID = strings.Repeat("c", 64)
	if _, err := ParseApplicationEvidence(command, resultFor(changed)); err == nil {
		t.Fatal("replacement runtime admitted")
	}
	changed = evidence
	changed.OperationID = "another-operation"
	if _, err := ParseApplicationEvidence(command, resultFor(changed)); err == nil {
		t.Fatal("another operation receipt admitted")
	}
}

func TestApplicationVerificationBindsSignedReceiptToCurrentSource(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY, ApplicationContainerId: id}
	evidence := ApplicationEvidence{APIVersion: "stackkit.application-adoption-result/v1", Authority: ApplicationAuthority{PlanHash: digest, LifecycleContractHash: digest, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", ProfileRef: HomeAssistantAdoptionProfile, Mode: "adopt_in_place", Source: ApplicationSource{ContainerID: id, ImageID: digest, ImageDigest: digest, Revision: digest, ConfigMount: "/srv/ha", ConfigurationDigest: digest, AccountsDigest: digest}, PlanDigest: digest, OperationID: "operation-adopt-1", OwnedFields: []string{"container.power-state"}, PreservedFields: []string{"accounts", "configuration", "persistentData"}, AdmittedOperations: []string{"verify", "start", "stop", "restart", "release"}, VerifiedOwnerRef: "ha-owner", Status: "verified", VerifiedAt: time.Now().UTC()}
	resultFor := func(source ApplicationSource, signatureVerified bool) *agentpb.StackKitResult {
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"apiVersion": "stackkit.application-adoption-verification/v1", "signatureVerified": signatureVerified, "currentSource": source, "receipt": map[string]any{"result": evidence, "signature": map[string]any{"ownerRef": "local-owner", "keyId": "key-1", "value": "signed"}}}})
		hash := sha256.Sum256(raw)
		return &agentpb.StackKitResult{Success: true, CommandResultJson: raw, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(hash[:])}
	}
	if got, err := ParseApplicationEvidence(command, resultFor(evidence.Source, true)); err != nil || got.OperationID != evidence.OperationID {
		t.Fatalf("verified wrapper failed: %v", err)
	}
	changed := evidence.Source
	changed.ImageID = "sha256:" + strings.Repeat("c", 64)
	if _, err := ParseApplicationEvidence(command, resultFor(changed, true)); err == nil {
		t.Fatal("verification admitted replacement image")
	}
	if _, err := ParseApplicationEvidence(command, resultFor(evidence.Source, false)); err == nil {
		t.Fatal("verification admitted unchecked signature")
	}
}

func TestApplicationControlRequiresDispatchedActionAndMeasuredOutcome(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL, ApplicationContainerId: id, ApplicationOperationId: "stop-1", ApplicationAction: "stop"}
	evidence := ApplicationEvidence{APIVersion: "stackkit.application-adoption-result/v1", Authority: ApplicationAuthority{PlanHash: digest, LifecycleContractHash: digest, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", ProfileRef: HomeAssistantAdoptionProfile, Mode: "adopt_in_place", Source: ApplicationSource{ContainerID: id, ImageID: digest, ImageDigest: digest, Revision: digest, ConfigMount: "/srv/ha", ConfigurationDigest: digest, AccountsDigest: digest}, PlanDigest: digest, OperationID: "stop-1", OwnedFields: []string{"container.power-state"}, PreservedFields: []string{"accounts", "configuration", "persistentData"}, AdmittedOperations: []string{"verify", "start", "stop", "restart", "release"}, VerifiedOwnerRef: "ha-owner", Status: "verified", VerifiedAt: time.Now().UTC(), Action: "stop", PowerState: "stopped"}
	resultFor := func(e ApplicationEvidence) *agentpb.StackKitResult {
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": e, "signature": map[string]any{"ownerRef": "local-owner", "keyId": "key-1", "value": "signed"}}})
		hash := sha256.Sum256(raw)
		return &agentpb.StackKitResult{Success: true, CommandResultJson: raw, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(hash[:])}
	}
	if _, err := ParseApplicationEvidence(command, resultFor(evidence)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"operation", "action", "power"} {
		changed := evidence
		switch field {
		case "operation":
			changed.OperationID = "other-stop"
		case "action":
			changed.Action = "start"
		case "power":
			changed.PowerState = "running"
		}
		if _, err := ParseApplicationEvidence(command, resultFor(changed)); err == nil {
			t.Fatalf("control admitted wrong %s", field)
		}
	}
}

func TestApplicationConnectorClosedMetadataRejectsRawCustodyAndAmbiguousKeys(t *testing.T) {
	request := ConnectorRequest{SchemaVersion: ApplicationConnectorRequestSchema}
	request.Connector.Binding = ConnectorSource{ResourceID: "service-1", ContainerID: strings.Repeat("a", 64), ImageDigest: "sha256:" + strings.Repeat("b", 64), ServiceRevision: 1, Node: ConnectorNode{SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"}}
	request.Connector.Action = "ensure"
	request.Connector.LifespanDays = 30
	raw, _ := json.Marshal(request)
	if _, err := DecodeConnectorRequest(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(strings.Replace(string(raw), `"schemaVersion":`, `"token":"private","schemaVersion":`, 1)), []byte(strings.Replace(string(raw), `"action":"ensure"`, `"action":"revoke","action":"ensure"`, 1)), append(append([]byte{}, raw...), raw...)} {
		if _, err := DecodeConnectorRequest(bad); err == nil {
			t.Fatal("ambiguous or raw custody metadata admitted")
		}
	}
}
