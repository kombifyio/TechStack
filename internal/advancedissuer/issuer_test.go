package advancedissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	wire "github.com/kombifyio/stackkits/contracts/advancedcapability"
)

// Conformance vectors are verified with the released central wire contract.

type memoryRecorder struct{ issuances []Issuance }

func (recorder *memoryRecorder) RecordIssuance(_ context.Context, issuance Issuance) error {
	recorder.issuances = append(recorder.issuances, issuance)
	return nil
}
func (*memoryRecorder) RecordTrustBinding(context.Context, TrustBinding) error { return nil }
func (*memoryRecorder) LookupTrustBinding(context.Context, string, string) (TrustBinding, error) {
	return TrustBinding{}, ErrTrustBindingMissing
}

func TestIssuedCapabilityUsesCentralWireAndRetainsIssuanceScope(t *testing.T) {
	t.Run("central codec accepts the shared conformance vector", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/advanced-capability-v1.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ PublicKeyBase64, KeyID string }
		vectorRaw, _ := os.ReadFile("testdata/advanced-capability-v1.vector.json")
		if err := json.Unmarshal(vectorRaw, &vector); err != nil {
			t.Fatal(err)
		}
		bundle := centralTrustBundle("techstack", vector.KeyID, vector.PublicKeyBase64)
		now := time.Date(2026, 7, 27, 15, 5, 0, 0, time.UTC)
		if err := verifyCentralCapabilityScope(bytes.TrimSpace(raw), bundle, now, "basement-main", "owner/local/00112233445566778899aabbccddeeff", OperationRollbackCoordinated); err != nil {
			t.Fatalf("shared conformance vector rejected by the central codec: %v", err)
		}
	})

	_, key, _ := ed25519.GenerateKey(nil)
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	recorder := &memoryRecorder{}
	issuer, err := New(Config{IssuerID: "techstack.0f1e2d3c", PrivateKey: key, Recorder: recorder, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	owner := "owner/local/" + hex.EncodeToString(bytes.Repeat([]byte{0xab}, 16))
	capability, err := issuer.Issue(context.Background(), Request{
		TenantID: "tenant-1", DeploymentID: "stack-1", StackID: "cloud-stack", OwnerRef: owner,
		Operations: []string{OperationTerramateChangeSetCreate, OperationDriftReconcileAdvanced},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	within := now.Add(time.Minute)
	if err := verifyCentralCapabilityScope(capability.Raw, issuer.TrustBundle(), within, "cloud-stack", owner, OperationTerramateChangeSetCreate); err != nil {
		t.Fatalf("issued capability rejected: %v\n%s", err, capability.Raw)
	}
	if err := verifyCentralCapabilityScope(capability.Raw, issuer.TrustBundle(), within, "other-stack", owner, OperationTerramateChangeSetCreate); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("capability for another stack = %v, want scope mismatch", err)
	}
	if err := verifyCentralCapabilityScope(capability.Raw, issuer.TrustBundle(), now.Add(DefaultTTL), "cloud-stack", owner, OperationTerramateChangeSetCreate); err == nil {
		t.Fatal("capability verified after its default lifetime")
	}
	if len(recorder.issuances) != 1 || recorder.issuances[0].CapabilityID != capability.ID || recorder.issuances[0].DeploymentID != "stack-1" {
		t.Fatalf("issuances = %+v, want the one capability recorded for stack-1", recorder.issuances)
	}
}

// The managed rollout requests and installs a kombify.me origin certificate
// through two capability-gated operations (ADR-0047). The issuer must issue
// them, or every managed rollout with kombify.me routes fails before apply.
func TestIssuedCapabilityAuthorizesTheOriginCertificateOperations(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	now := time.Date(2026, 10, 4, 15, 0, 0, 0, time.UTC)
	issuer, err := New(Config{IssuerID: "techstack.0f1e2d3c", PrivateKey: key, Recorder: &memoryRecorder{}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	owner := "owner/local/" + hex.EncodeToString(bytes.Repeat([]byte{0xcd}, 16))
	capability, err := issuer.Issue(context.Background(), Request{
		TenantID: "tenant-1", DeploymentID: "stack-1", StackID: "cloud-stack", OwnerRef: owner,
		Operations: []string{OperationOriginCertificateRequest, OperationOriginCertificateInstall},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, operation := range []string{OperationOriginCertificateRequest, OperationOriginCertificateInstall} {
		if err := verifyCentralCapabilityScope(capability.Raw, issuer.TrustBundle(), now.Add(time.Minute), "cloud-stack", owner, operation); err != nil {
			t.Fatalf("capability for %s rejected: %v", operation, err)
		}
	}
}

var (
	errMalformed     = errors.New("malformed")
	errUntrusted     = errors.New("untrusted key")
	errSignature     = errors.New("signature invalid")
	errLifetime      = errors.New("outside lifetime")
	errScopeMismatch = errors.New("scope mismatch")
)

func verifyCentralCapabilityScope(raw, bundleRaw []byte, now time.Time, stackID, ownerRef, operation string) error {
	bundle, err := wire.DecodeTrustBundle(bundleRaw)
	if err != nil {
		return errMalformed
	}
	document, err := wire.DecodeCapability(raw)
	if err != nil {
		return errMalformed
	}
	key, ok := bundle.PublicKey(document.IssuerID, document.KeyID)
	if !ok {
		return errUntrusted
	}
	if err := document.VerifySignature(key); err != nil {
		return errSignature
	}
	if document.IssuedAtTime().After(now.Add(5*time.Minute)) || !document.ExpiresAtTime().After(now) {
		return errLifetime
	}
	if document.StackID != stackID || document.OwnerRef != ownerRef || !slices.Contains(document.AllowedOperations, operation) {
		return errScopeMismatch
	}
	return nil
}
func centralTrustBundle(issuerID, keyID, publicKey string) []byte {
	return (wire.TrustBundle{SchemaVersion: wire.TrustBundleSchemaVersion, Keys: []wire.TrustBundleKey{{IssuerID: issuerID, KeyID: keyID, PublicKey: publicKey}}}).Canonical()
}
