package advancedissuer

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBackupRenewalIssuedOnlyForExactAdmittedRestore(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	recorder := &memoryRecorder{}
	issuer, err := New(Config{IssuerID: "techstack", PrivateKey: key, Recorder: recorder, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := func(prefix string) string { return prefix + "://sha256/" + strings.Repeat("b", 64) }
	renewal := &BackupRenewal{AgentID: "agent-1", BindingHash: digest, CustodyAttestationRef: ref("backup-custody-attestation"), DeploymentID: "deployment-1", FreshAttestationRef: ref("backup-custody-attestation"), JobID: "job-drill-1", MeasuredAt: now.Format(time.RFC3339), PlanHash: digest, QuotaBytes: "100", RepositoryID: "kopia:local:cloud", TargetRef: ref("backup-target"), TenantID: "tenant-1", UsedBytes: "20"}
	request := Request{TenantID: "tenant-1", DeploymentID: "deployment-1", StackID: "cloud-stack", OwnerRef: "owner/local/00112233445566778899aabbccddeeff", Operations: []string{OperationRestoreDrill}, BackupRenewal: renewal}
	capability, err := issuer.Issue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Public conformance vector is also verified by StackKits' actual verifier.
	raw, err := os.ReadFile("testdata/backup-renewal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector envelope
	if json.Unmarshal(raw, &vector) != nil || !bytes.Equal(raw, canonicalDocument(vector)) {
		t.Fatal("renewal conformance serialization diverged")
	}
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), SigningDigest(canonicalUnsigned(vector)), decodeTestSignature(t, vector.Signature)) {
		t.Fatal("cross-repository renewal signature diverged")
	}
	if len(capability.Raw) == 0 || recorder.issuances[0].CapabilityID != capability.ID {
		t.Fatal("issued authority was not recorded")
	}
	for _, change := range []string{"tenant", "operation", "quota", "stale", "lifetime"} {
		next := request
		r := *renewal
		next.BackupRenewal = &r
		switch change {
		case "tenant":
			next.TenantID = "other"
		case "operation":
			next.Operations = []string{OperationRollbackCoordinated}
		case "quota":
			r.UsedBytes = "100"
		case "stale":
			r.MeasuredAt = now.Add(-6 * time.Minute).Format(time.RFC3339)
		case "lifetime":
			next.TTL = 16 * time.Minute
		}
		if _, err := issuer.Issue(t.Context(), next); err == nil {
			t.Fatalf("issued denied renewal: %s", change)
		}
	}
}

func decodeTestSignature(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
