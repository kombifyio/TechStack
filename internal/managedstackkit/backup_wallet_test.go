package managedstackkit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
)

// fakeWallet seals and signs like the kombify Wallet: only to a node whose
// statement pins its delivering key and that the item lists as a host target.
type fakeWallet struct {
	key     ed25519.PrivateKey
	keyID   string
	stored  map[string][]byte
	targets map[string]HostTarget
}

func newFakeWallet(t *testing.T) *fakeWallet {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeWallet{key: key, keyID: "wallet-host-delivery-1", stored: map[string][]byte{}, targets: map[string]HostTarget{}}
}

func (w *fakeWallet) public() ed25519.PublicKey { return w.key.Public().(ed25519.PublicKey) }

func (w *fakeWallet) Authority() (string, string, error) {
	return base64.RawStdEncoding.EncodeToString(w.public()), w.keyID, nil
}

func (w *fakeWallet) StoreBackupTarget(_ context.Context, _, _ string, material []byte) (string, error) {
	itemID := "itm_" + strings.Repeat("a", 26)
	w.stored[itemID] = append([]byte(nil), material...)
	return itemID, nil
}

func (w *fakeWallet) DeliverBackupTarget(_ context.Context, _, itemID, operation string, target HostTarget, raw []byte, challenge string, expiresAt time.Time) ([]byte, error) {
	listed, ok := w.targets[itemID]
	if !ok || listed != target {
		return nil, errors.New("host_target_unknown")
	}
	var statement hostdelivery.Statement
	if err := json.Unmarshal(raw, &statement); err != nil {
		return nil, err
	}
	owner, _ := base64.RawStdEncoding.DecodeString(listed.OwnerPublicKey)
	if err := hostdelivery.VerifyStatement(statement, owner, challenge, time.Now(), 10*time.Minute); err != nil {
		return nil, err
	}
	if err := hostdelivery.VerifyStatementAuthority(statement, w.public(), w.keyID); err != nil {
		return nil, errors.New("host_authority_not_pinned")
	}
	recipient, _ := base64.RawStdEncoding.DecodeString(statement.RecipientKey)
	now := time.Now().UTC()
	unsigned, err := hostdelivery.Seal(recipient, hostdelivery.Context{
		TenantRef: target.TenantRef, DeliveryRef: itemID, ItemVersion: 1, SiteRef: statement.SiteRef, NodeRef: statement.NodeRef,
		BindingGeneration: statement.Generation, Operation: operation, RecipientFingerprint: statement.Fingerprint, OperationID: "op-1",
		IssuedAt: now.Format(time.RFC3339Nano), NotAfter: expiresAt.UTC().Format(time.RFC3339Nano),
	}, w.stored[itemID], now)
	if err != nil {
		return nil, err
	}
	return hostdelivery.SignEnvelope(unsigned, w.key, w.keyID)
}

// pinnedStatement is the node's statement after it pinned authority.
func (n node) pinnedStatement(t *testing.T, challenge string, authority ed25519.PublicKey, keyID string) []byte {
	t.Helper()
	var statement hostdelivery.Statement
	if err := json.Unmarshal(n.statement(t, challenge, time.Now()), &statement); err != nil {
		t.Fatal(err)
	}
	statement.AuthorityKeyID, statement.AuthorityFingerprint, statement.Signature = keyID, hostdelivery.Fingerprint(authority), hostdelivery.Signature{}
	payload, err := statement.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	statement.Signature = hostdelivery.Signature{OwnerRef: "owner/local/1", KeyID: "owner-1", Value: base64.RawStdEncoding.EncodeToString(ed25519.Sign(n.owner, hostdelivery.StatementDigest(payload)))}
	encoded, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// In Wallet mode the managed backup target reaches the node only as the
// Wallet's authority-signed delivery: it verifies under the key the node
// pinned and opens there to the S3 target object, and nothing is sealed by
// Techstack itself, not even when the Wallet is missing or the node has not
// pinned its key.
func TestWalletModeDeliversTheBackupTargetOnlyThroughTheWallet(t *testing.T) {
	ctx := context.Background()
	scope := DeliveryScope{TenantID: "tenant-a", StackID: "stack-a", OwnerID: "auth0|synthetic-owner"}
	wallet := newFakeWallet(t)
	custody := &fakeDeliveryCustody{}
	delivery, err := NewBackupDelivery(custody, custody, WithWallet(wallet))
	if err != nil {
		t.Fatal(err)
	}
	delivery.attest = func(_ context.Context, _ backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
		evidence.ObservedAt = time.Now()
		return evidence, nil
	}
	node := newNode(t)
	nodeOwner := base64.RawStdEncoding.EncodeToString(node.owner.Public().(ed25519.PublicKey))
	wallet.targets["itm_"+strings.Repeat("a", 26)] = HostTarget{SiteRef: cloudOperationsSite, NodeRef: cloudOperationsNode, TenantRef: stackkitcommand.DeliveryTenantRef(scope.TenantID), OwnerPublicKey: nodeOwner}

	// A node that has not pinned the Wallet's key gets nothing, and nothing is stored.
	challenge, _ := delivery.NewChallenge()
	unpinned, err := delivery.AdmitRecipient(ctx, scope, challenge, node.statement(t, challenge, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if sealed, err := delivery.Seal(ctx, scope, unpinned, hostdelivery.OperationBackupTargetImport); err == nil || sealed != nil || len(wallet.stored) != 0 {
		t.Fatalf("a node without the pinned Wallet key received a delivery: %v", err)
	}

	challenge, _ = delivery.NewChallenge()
	recipient, err := delivery.AdmitRecipient(ctx, scope, challenge, node.pinnedStatement(t, challenge, wallet.public(), wallet.keyID))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{hostdelivery.OperationBackupTargetImport, hostdelivery.OperationBackupTargetRebind} {
		sealed, err := delivery.Seal(ctx, scope, recipient, operation)
		if err != nil {
			t.Fatalf("%s through the Wallet: %v", operation, err)
		}
		envelope, err := hostdelivery.ParseEnvelope(sealed)
		if err != nil || envelope.VerifyAuthority(wallet.public(), wallet.keyID) != nil {
			t.Fatalf("%s is not signed by the pinned Wallet key: %v", operation, err)
		}
		opened, plaintext, err := hostdelivery.Open(node.recipient.Bytes(), sealed, time.Now())
		var material map[string]string
		if err != nil || json.Unmarshal(plaintext, &material) != nil || opened.Operation != operation ||
			material["secretAccessKey"] != "SECRET-ACCESS-KEY" || material["passphrase"] != "REPO-PASSPHRASE" || strings.Contains(string(sealed), "SECRET-ACCESS-KEY") {
			t.Fatalf("%s did not open on the node to the backup target: %v", operation, err)
		}
	}

	// Wallet mode without a configured Wallet fails closed instead of sealing.
	unconfigured, err := NewBackupDelivery(custody, custody, WithWallet(nil))
	if err != nil {
		t.Fatal(err)
	}
	if sealed, err := unconfigured.Seal(ctx, scope, recipient, hostdelivery.OperationBackupTargetImport); !errors.Is(err, ErrBackupWalletUnavailable) || sealed != nil {
		t.Fatalf("Wallet mode without a Wallet sealed itself: %v", err)
	}
}
