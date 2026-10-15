package routes

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// Each worker signs with its own key: a binding signed for one worker is
// refused by every other worker's Guard, and a later pairing of the same worker
// names the key its Guard already pinned.
func TestEachWorkerGuardAcceptsOnlyItsOwnBindings(t *testing.T) {
	h := workerRouteHandlers{bindingKeyBase: []byte("0123456789abcdef0123456789abcdef")}
	pins := func(tenantID, workerID string) []guardbinding.PinnedKey {
		signer, err := h.bindingSignerFor(tenantID, workerID)
		if err != nil {
			t.Fatal(err)
		}
		return signer.PublicKeys()
	}
	pinnedA, otherWorker, otherTenant := pins("tenant-1", "worker-a"), pins("tenant-1", "worker-b"), pins("tenant-2", "worker-a")
	signer, err := h.bindingSignerFor("tenant-1", "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	binding := guardbinding.Binding{
		CommandID: "cmd-1", TenantID: "tenant-1", WorkerID: "worker-a", ServerID: "server-a",
		Operation: "STACKKIT_OPERATION_APPLY", PayloadDigest: "digest", WorkerGeneration: 1, CurrentGeneration: 1,
		AuthorityExpiresAt: time.Now().Add(time.Minute), ContractVersion: guardbinding.ContractVersion,
	}
	if err := signer.Sign(&binding); err != nil {
		t.Fatal(err)
	}
	if denial := binding.VerifySignature(pinnedA); denial != nil {
		t.Fatalf("worker A's Guard refused its own binding: %v", denial)
	}
	if binding.VerifySignature(otherWorker) == nil {
		t.Fatal("another worker's Guard accepted a binding signed for worker A")
	}
	if binding.VerifySignature(otherTenant) == nil {
		t.Fatal("a same-named worker of another tenant accepted a binding signed for worker A")
	}
}
