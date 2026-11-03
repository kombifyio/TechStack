package stackkitrelease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCapabilityBoundToBinaryDigest(t *testing.T) {
	old := strings.NewReader("old release without capability declaration")
	before, err := digestArchiveReader(old, int64(old.Len()))
	if err != nil || before.verifyExpectedPlan {
		t.Fatalf("old binary admitted: %v", err)
	}
	binary := "prefix" + verifyPlanManifest + "suffix"
	input := strings.NewReader(binary)
	actual, err := digestArchiveReader(input, int64(input.Len()))
	if err != nil || !actual.verifyExpectedPlan || actual.sha256 == before.sha256 {
		t.Fatalf("new binary not bound to digest: %v", err)
	}
	scanner := &capabilityScanner{}
	marker := []byte(verifyPlanManifest)
	_, _ = scanner.Write(marker[:11])
	_, _ = scanner.Write(marker[11:])
	if scanner.matches != 1 {
		t.Fatal("declaration split across reads was missed")
	}
	duplicate := strings.NewReader(binary + binary)
	duplicateDigest, err := digestArchiveReader(duplicate, int64(duplicate.Len()))
	if err != nil || duplicateDigest.verifyExpectedPlan {
		t.Fatal("ambiguous duplicate declaration admitted")
	}
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte(binary), 0600); err != nil {
		t.Fatal(err)
	}
	admitted, err := verifyCapabilityBinary(path, actual.sha256)
	if err != nil || !admitted {
		t.Fatalf("matching binary refused: %v", err)
	}
	if _, err := verifyCapabilityBinary(path, before.sha256); err == nil {
		t.Fatal("binary admitted against a different signed digest")
	}
	missing, err := verifyCapabilityBinary(path+"-missing", actual.sha256)
	if err != nil || missing {
		t.Fatal("missing legacy binary granted capability")
	}
}

func TestRILPrimitiveCapabilityRequiresUniqueDigestBoundMarker(t *testing.T) {
	const hash = "sha256:a745b4b217392a05de34b757b98ee04ff66d3c9ad6debb68dc2425f79e240df0"
	marker := "stackkit.ril-primitive-contract/v1:" + hash + ":end-stackkit.ril-primitive-contract"
	scanner := &capabilityScanner{}
	raw := []byte(rilPrimitiveManifest + marker)
	for _, b := range raw {
		_, _ = scanner.Write([]byte{b})
	}
	if scanner.rilMatches != 1 || scanner.qualifiedHash() != hash {
		t.Fatal("split signed capability was not retained")
	}
	_, _ = scanner.Write([]byte(marker))
	if scanner.qualifiedHash() != "" {
		t.Fatal("duplicate primitive authority was admitted")
	}
	old := strings.NewReader(verifyPlanManifest)
	digest, err := digestArchiveReader(old, int64(old.Len()))
	if err != nil || digest.verifyRILPrimitive || digest.rilPrimitiveHash != "" {
		t.Fatal("old binary gained RIL primitive capability")
	}
}
