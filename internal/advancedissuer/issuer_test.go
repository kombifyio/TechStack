package advancedissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The verification below is a minimal port of the StackKits verifier
// (internal/advancedcapability/verifier.go and canonical.go, and
// internal/advancedtrust/bundle.go in the pinned StackKits release). StackKits
// is the authority; the port is anchored to its published conformance vector
// in testdata/, copied unchanged from StackKits.

type memoryRecorder struct{ issuances []Issuance }

func (recorder *memoryRecorder) RecordIssuance(_ context.Context, issuance Issuance) error {
	recorder.issuances = append(recorder.issuances, issuance)
	return nil
}
func (*memoryRecorder) RecordTrustBinding(context.Context, TrustBinding) error { return nil }
func (*memoryRecorder) LookupTrustBinding(context.Context, string, string) (TrustBinding, error) {
	return TrustBinding{}, ErrTrustBindingMissing
}

func TestIssuedCapabilityVerifiesUnderStackKitsRulesForItsStackOnly(t *testing.T) {
	t.Run("port accepts the StackKits conformance vector", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/advanced-capability-v1.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ PublicKeyBase64, KeyID string }
		vectorRaw, _ := os.ReadFile("testdata/advanced-capability-v1.vector.json")
		if err := json.Unmarshal(vectorRaw, &vector); err != nil {
			t.Fatal(err)
		}
		bundle := stackKitsCanonicalBundle("techstack", vector.KeyID, vector.PublicKeyBase64)
		now := time.Date(2026, 7, 27, 15, 5, 0, 0, time.UTC)
		if err := stackKitsVerify(bytes.TrimSpace(raw), bundle, now, "basement-main", "owner/local/00112233445566778899aabbccddeeff", OperationRollbackCoordinated); err != nil {
			t.Fatalf("StackKits conformance vector rejected by the port: %v", err)
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
	if err := stackKitsVerify(capability.Raw, issuer.TrustBundle(), within, "cloud-stack", owner, OperationTerramateChangeSetCreate); err != nil {
		t.Fatalf("issued capability rejected: %v\n%s", err, capability.Raw)
	}
	if err := stackKitsVerify(capability.Raw, issuer.TrustBundle(), within, "other-stack", owner, OperationTerramateChangeSetCreate); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("capability for another stack = %v, want scope mismatch", err)
	}
	if err := stackKitsVerify(capability.Raw, issuer.TrustBundle(), now.Add(DefaultTTL), "cloud-stack", owner, OperationTerramateChangeSetCreate); err == nil {
		t.Fatal("capability verified after its default lifetime")
	}
	if len(recorder.issuances) != 1 || recorder.issuances[0].CapabilityID != capability.ID || recorder.issuances[0].DeploymentID != "stack-1" {
		t.Fatalf("issuances = %+v, want the one capability recorded for stack-1", recorder.issuances)
	}
}

var (
	errMalformed     = errors.New("malformed")
	errUntrusted     = errors.New("untrusted key")
	errSignature     = errors.New("signature invalid")
	errLifetime      = errors.New("outside lifetime")
	errScopeMismatch = errors.New("scope mismatch")
)

type portEnvelope struct {
	AllowedOperations []string `json:"allowedOperations"`
	Audience          string   `json:"audience"`
	CapabilityID      string   `json:"capabilityId"`
	ExpiresAt         string   `json:"expiresAt"`
	IssuedAt          string   `json:"issuedAt"`
	IssuerID          string   `json:"issuerId"`
	KeyID             string   `json:"keyId"`
	OwnerRef          string   `json:"ownerRef"`
	RILRef            string   `json:"rilRef"`
	SchemaVersion     string   `json:"schemaVersion"`
	Signature         string   `json:"signature"`
	StackID           string   `json:"stackId"`
	UIManagerRef      string   `json:"uiManagerRef"`
}

func stackKitsVerify(raw, bundleRaw []byte, now time.Time, stackID, ownerRef, operation string) error {
	var bundle struct {
		Keys []struct {
			IssuerID  string `json:"issuerId"`
			KeyID     string `json:"keyId"`
			PublicKey string `json:"publicKey"`
		} `json:"keys"`
		SchemaVersion string `json:"schemaVersion"`
	}
	decoder := json.NewDecoder(bytes.NewReader(bundleRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bundle) != nil || bundle.SchemaVersion != TrustBundleSchemaVersion || len(bundle.Keys) != 1 {
		return errMalformed
	}
	key := bundle.Keys[0]
	if !bytes.Equal(bundleRaw, stackKitsCanonicalBundle(key.IssuerID, key.KeyID, key.PublicKey)) {
		return errMalformed
	}
	publicKey, err := base64.RawStdEncoding.Strict().DecodeString(key.PublicKey)
	digest := sha256.Sum256(publicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || key.KeyID != "ed25519://sha256/"+hex.EncodeToString(digest[:]) {
		return errMalformed
	}

	var document portEnvelope
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || document.SchemaVersion != CapabilitySchemaVersion || document.Audience != Audience ||
		!slices.IsSorted(document.AllowedOperations) || len(document.AllowedOperations) == 0 ||
		document.UIManagerRef == "" || document.RILRef == "" {
		return errMalformed
	}
	if document.KeyID != key.KeyID || document.IssuerID != key.IssuerID {
		return errUntrusted
	}
	signature, err := base64.RawStdEncoding.DecodeString(document.Signature)
	if err != nil || !ed25519.Verify(publicKey, stackKitsSigningDigest(stackKitsCanonical(document, false)), signature) {
		return errSignature
	}
	if !bytes.Equal(raw, stackKitsCanonical(document, true)) {
		return errMalformed
	}
	issuedAt, issuedErr := time.Parse(time.RFC3339, document.IssuedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339, document.ExpiresAt)
	if issuedErr != nil || expiresErr != nil || issuedAt.After(now.Add(5*time.Minute)) || !expiresAt.After(now) ||
		expiresAt.Sub(issuedAt) > 30*24*time.Hour {
		return errLifetime
	}
	if document.StackID != stackID || document.OwnerRef != ownerRef || !slices.Contains(document.AllowedOperations, operation) {
		return errScopeMismatch
	}
	return nil
}

// stackKitsCanonical reproduces StackKits canonicalUnsigned/canonicalDocument:
// RFC 8785 key order with "signature" between "schemaVersion" and "stackId".
func stackKitsCanonical(document portEnvelope, signed bool) []byte {
	out := []byte(`{"allowedOperations":[`)
	for index, operation := range document.AllowedOperations {
		if index > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendQuote(out, operation)
	}
	out = append(out, ']')
	fields := [][2]string{
		{"audience", document.Audience}, {"capabilityId", document.CapabilityID}, {"expiresAt", document.ExpiresAt},
		{"issuedAt", document.IssuedAt}, {"issuerId", document.IssuerID}, {"keyId", document.KeyID},
		{"ownerRef", document.OwnerRef}, {"rilRef", document.RILRef}, {"schemaVersion", document.SchemaVersion},
	}
	if signed {
		fields = append(fields, [2]string{"signature", document.Signature})
	}
	fields = append(fields, [2]string{"stackId", document.StackID}, [2]string{"uiManagerRef", document.UIManagerRef})
	for _, field := range fields {
		out = append(out, ',')
		out = strconv.AppendQuote(out, field[0])
		out = append(out, ':')
		out = strconv.AppendQuote(out, field[1])
	}
	return append(out, '}')
}

func stackKitsSigningDigest(unsigned []byte) []byte {
	digest := sha256.Sum256(append(append([]byte(CapabilitySchemaVersion), 0), unsigned...))
	return digest[:]
}

func stackKitsCanonicalBundle(issuerID, keyID, publicKey string) []byte {
	out := []byte(`{"keys":[{"issuerId":`)
	out = strconv.AppendQuote(out, issuerID)
	out = append(out, `,"keyId":`...)
	out = strconv.AppendQuote(out, keyID)
	out = append(out, `,"publicKey":`...)
	out = strconv.AppendQuote(out, publicKey)
	out = append(out, `}],"schemaVersion":`...)
	out = strconv.AppendQuote(out, TrustBundleSchemaVersion)
	return append(out, '}')
}
