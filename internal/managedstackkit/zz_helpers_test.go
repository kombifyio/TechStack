package managedstackkit

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"github.com/kombifyio/techstack/pkg/backupstore"
	"encoding/base64"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
	"encoding/json"
	"crypto/rand"
	"testing"
	"time"
)

type fakeDeliveryCustody struct {
	pin     *backupstore.RecipientPin
	binding *backupstore.NodeBinding
}

func (*fakeDeliveryCustody) Ensure(context.Context, string, string) (backupstore.CustodyEvidence, error) {
	return backupstore.CustodyEvidence{BindingEvidence: []byte("b"), TargetEvidence: []byte("t"), ObservedAt: time.Now()}, nil
}

func (*fakeDeliveryCustody) Get(context.Context, string, string) (backupstore.Credentials, error) {
	return backupstore.Credentials{TenantID: "tenant-a", StackID: "stack-a", Bucket: "skbk-stack-a", Endpoint: "https://acct.r2.cloudflarestorage.com", TokenID: "token-a", AccessKeyID: "access-a", SecretAccessKey: "SECRET-ACCESS-KEY", RepositoryPassword: "REPO-PASSPHRASE"}, nil
}

func (*fakeDeliveryCustody) Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error) {
	return backupstore.CustodyEvidence{BindingEvidence: []byte("b"), TargetEvidence: []byte("t")}, nil
}

func (*fakeDeliveryCustody) RecordAttestation(_ context.Context, _, _ string, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
	return evidence, nil
}

func (f *fakeDeliveryCustody) PinRecipient(_ context.Context, pin backupstore.RecipientPin) error {
	if f.pin != nil && (f.pin.Fingerprint != pin.Fingerprint || f.pin.OwnerKey != pin.OwnerKey || f.pin.Generation != pin.Generation) {
		return backupstore.ErrRecipientSubstituted
	}
	f.pin = &pin
	return nil
}

func (f *fakeDeliveryCustody) Recipient(context.Context, string, string) (backupstore.RecipientPin, bool, error) {
	if f.pin == nil {
		return backupstore.RecipientPin{}, false, nil
	}
	return *f.pin, true, nil
}

func (f *fakeDeliveryCustody) RecordNodeBinding(_ context.Context, _, _ string, binding backupstore.NodeBinding) error {
	f.binding = &binding
	return nil
}

func (f *fakeDeliveryCustody) NodeBinding(context.Context, string, string) (backupstore.NodeBinding, error) {
	return *f.binding, nil
}

// node is a StackKits node: an owner signing key and a separate recipient key.
type node struct {
	owner     ed25519.PrivateKey
	recipient *ecdh.PrivateKey
}

func newNode(t *testing.T) node {
	t.Helper()
	_, owner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return node{owner: owner, recipient: recipient}
}

func (n node) statement(t *testing.T, challenge string, issuedAt time.Time) []byte {
	t.Helper()
	public := n.recipient.PublicKey().Bytes()
	statement := hostdelivery.Statement{
		APIVersion: hostdelivery.StatementAPIVersion, SiteRef: cloudOperationsSite, NodeRef: cloudOperationsNode,
		RecipientKey: base64.RawStdEncoding.EncodeToString(public), Fingerprint: hostdelivery.Fingerprint(public), Generation: 1,
		Challenge: challenge, IssuedAt: issuedAt.UTC().Format(time.RFC3339Nano), OwnerKeyID: "owner-1",
		OwnerPublic: base64.RawStdEncoding.EncodeToString(n.owner.Public().(ed25519.PublicKey)),
	}
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

