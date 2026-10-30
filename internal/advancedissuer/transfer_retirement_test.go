package advancedissuer

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	wire "github.com/kombifyio/techstack/internal/selfhostcontracts/advancedcapability"
)

func TestTransferRetirementIssuanceRetainsExactScopeAndRejectsWidening(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC)
	recorder := &memoryRecorder{}
	issuer, err := New(Config{IssuerID: "techstack", PrivateKey: key, Recorder: recorder, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	scope := wire.TransferRetirement{BaselineSpecHash: "sha256:" + strings.Repeat("a", 64), CandidateSpecHash: "sha256:" + strings.Repeat("b", 64), OperationID: "snapshot.Transfer-1", OutcomeDigest: "sha256:" + strings.Repeat("c", 64), Role: "source", TransferID: "sha256:" + strings.Repeat("d", 64)}
	request := Request{TenantID: "tenant-1", DeploymentID: "deployment-1", StackID: "cloud-stack", OwnerRef: "owner/local/00112233445566778899aabbccddeeff", Operations: []string{OperationApplicationTransferRetire}, TransferRetirement: &scope}
	issued, err := issuer.Issue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	document, err := wire.DecodeCapability(issued.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if document.TransferRetirement == nil || *document.TransferRetirement != scope {
		t.Fatal("issuer changed the admitted transfer scope")
	}
	if err = document.VerifySignature(key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.issuances) != 1 || recorder.issuances[0].CapabilityID != document.CapabilityID || recorder.issuances[0].DeploymentID != request.DeploymentID {
		t.Fatal("authority escaped without its existing issuance record")
	}
	for name, change := range map[string]func(*Request){
		"scope omitted":   func(r *Request) { r.TransferRetirement = nil },
		"other operation": func(r *Request) { r.Operations = []string{OperationRollbackCoordinated} },
		"multiple operations": func(r *Request) {
			r.Operations = []string{OperationApplicationTransferRetire, OperationRollbackCoordinated}
		},
		"long lifetime":    func(r *Request) { r.TTL = 16 * time.Minute },
		"unsafe role":      func(r *Request) { r.TransferRetirement.Role = "any" },
		"unchanged intent": func(r *Request) { r.TransferRetirement.CandidateSpecHash = r.TransferRetirement.BaselineSpecHash },
	} {
		t.Run(name, func(t *testing.T) {
			next := request
			copy := scope
			next.TransferRetirement = &copy
			change(&next)
			if _, err := issuer.Issue(t.Context(), next); err == nil {
				t.Fatal("issuer accepted widened retirement")
			}
			if len(recorder.issuances) != 1 {
				t.Fatal("denied issuance changed the ledger")
			}
		})
	}
}
