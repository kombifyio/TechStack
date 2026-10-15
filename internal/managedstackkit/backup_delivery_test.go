package managedstackkit

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
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

func newDelivery(t *testing.T) (*BackupDelivery, *fakeDeliveryCustody) {
	t.Helper()
	custody := &fakeDeliveryCustody{}
	delivery, err := NewBackupDelivery(custody, custody)
	if err != nil {
		t.Fatal(err)
	}
	delivery.attest = func(_ context.Context, _ backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
		evidence.AttestationEvidence = []byte("sha256:" + strings.Repeat("c", 64))
		evidence.ObservedAt = time.Now()
		return evidence, nil
	}
	return delivery, custody
}

// Only the node that proved the recipient key can open what Core seals, nothing
// readable leaves the control plane, and a substituted key is never sealed to.
func TestBackupCredentialsOpenOnlyForTheNodeThatProvedItsRecipientKey(t *testing.T) {
	ctx := context.Background()
	scope := DeliveryScope{TenantID: "tenant-a", StackID: "stack-a"}
	delivery, _ := newDelivery(t)
	real := newNode(t)
	challenge, err := delivery.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := delivery.AdmitRecipient(ctx, scope, challenge, real.statement(t, challenge, time.Now()))
	if err != nil {
		t.Fatalf("the enrolled node was refused: %v", err)
	}
	sealed, err := delivery.Seal(ctx, scope, recipient, hostdelivery.OperationBackupTargetImport)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-ACCESS-KEY", "REPO-PASSPHRASE", "acct.r2.cloudflarestorage.com", "skbk-stack-a"} {
		if strings.Contains(string(sealed), secret) {
			t.Fatalf("the sealed delivery carries %q in the clear", secret)
		}
	}
	if _, opened, err := hostdelivery.Open(real.recipient.Bytes(), sealed, time.Now()); err != nil || !strings.Contains(string(opened), "SECRET-ACCESS-KEY") {
		t.Fatalf("the node could not open its delivery: %v", err)
	}
	if _, _, err := hostdelivery.Open(newNode(t).recipient.Bytes(), sealed, time.Now()); err == nil {
		t.Fatal("another node opened the delivery")
	}

	for name, statement := range map[string][]byte{
		"a different recipient key under the pinned owner key": func() []byte {
			substitute := real
			substitute.recipient = newNode(t).recipient
			return substitute.statement(t, challenge, time.Now())
		}(),
		"a different owner key": func() []byte { n := newNode(t); return n.statement(t, challenge, time.Now()) }(),
		"a stale statement":     real.statement(t, challenge, time.Now().Add(-time.Hour)),
		"another challenge":     real.statement(t, "another-challenge", time.Now()),
	} {
		if _, err := delivery.AdmitRecipient(ctx, scope, challenge, statement); err == nil {
			t.Fatalf("%s was admitted", name)
		}
	}
}

// Regression (managed Cloud lane 2026-10-03): a real tenant ID carries an Auth0
// subject ("usr:google-oauth2|…"); the agent refused it as the import's
// --tenant-ref. The sealed context and the import argument must name the same
// bare reference, and the subject must not reach the node's command line.
func TestSealedDeliveryAddressesABareTenantRefForAnAuth0Tenant(t *testing.T) {
	ctx := context.Background()
	tenantID := "usr:google-oauth2|fictional-backup-tenant"
	scope := DeliveryScope{TenantID: tenantID, StackID: "stack-a"}
	delivery, _ := newDelivery(t)
	node := newNode(t)
	challenge, err := delivery.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := delivery.AdmitRecipient(ctx, scope, challenge, node.statement(t, challenge, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := delivery.Seal(ctx, scope, recipient, hostdelivery.OperationBackupTargetImport)
	if err != nil {
		t.Fatal(err)
	}
	opened, _, err := hostdelivery.Open(node.recipient.Bytes(), sealed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	importRef := stackkitcommand.DeliveryTenantRef(tenantID)
	// The agent's argv rule for --tenant-ref (pkg/agent stackKitDeliveryTokenPattern).
	bare := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	if opened.TenantRef != importRef || !bare.MatchString(importRef) || strings.Contains(string(sealed), "google-oauth2") {
		t.Fatalf("sealed tenant %q, import tenant %q: not one bare reference", opened.TenantRef, importRef)
	}
}
