package routes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/httpx"
)

func writeSignedRelease(t *testing.T, dir, binary, manifestDigest string) {
	t.Helper()
	manifest, err := json.Marshal(guardrelease.Manifest{
		SchemaVersion: guardrelease.ManifestSchema, Version: "0.50.0", MinimumAdmittedVersion: "0.48.0",
		Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: manifestDigest, Size: int64(len(binary))}},
		IssuedAt:  time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		guardReleaseManifestFile:              manifest,
		guardReleaseManifestSig:               []byte(`{"mediaType":"manifest-bundle"}`),
		"techstack-linux-amd64":               []byte(binary),
		"techstack-linux-amd64.sigstore.json": []byte(`{"mediaType":"artifact-bundle"}`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The control plane serves the signed release it was deployed with, and only
// when manifest and binary agree; a release that names other bytes than the
// ones it would serve is unavailable rather than sent with evidence that cannot
// match. Without a configured release it offers none.
func TestControlPlaneServesOnlyAConsistentSignedGuardRelease(t *testing.T) {
	handler, _ := newAgentBinaryTestHandler(t, "unsigned-running-binary", "amd64")
	handler.binaryArtifact = nil // the default provider: the running executable, or the signed release
	router := httpx.NewRouter()
	router.POST("/api/v1/agent/release-evidence/{os}/{arch}", handler.agentReleaseEvidence)
	router.POST("/api/v1/agent/binary/{os}/{arch}", handler.agentBinary)
	call := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	t.Setenv(guardReleaseDirEnv, "")
	if recorder := call("/api/v1/agent/release-evidence/linux/amd64"); recorder.Code != http.StatusNotFound {
		t.Fatalf("a control plane without a signed release answered %d", recorder.Code)
	}

	dir := t.TempDir()
	t.Setenv(guardReleaseDirEnv, dir)
	sum := sha256.Sum256([]byte("signed-guard-binary"))
	writeSignedRelease(t, dir, "signed-guard-binary", hex.EncodeToString(sum[:]))
	recorder := call("/api/v1/agent/release-evidence/linux/amd64")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Version        string          `json:"version"`
			SHA256         string          `json:"sha256"`
			Manifest       string          `json:"manifest"`
			ManifestBundle json.RawMessage `json:"manifest_bundle"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := base64.StdEncoding.DecodeString(envelope.Data.Manifest)
	if err != nil || envelope.Data.SHA256 != hex.EncodeToString(sum[:]) || envelope.Data.Version != "0.50.0" || len(envelope.Data.ManifestBundle) == 0 {
		t.Fatalf("evidence = %+v err = %v", envelope.Data, err)
	}
	if parsed, err := guardrelease.ParseManifest(manifestRaw); err != nil || parsed.Version != "0.50.0" {
		t.Fatalf("the served manifest is not the signed bytes: %v", err)
	}
	// The binary endpoint serves the signed file, not the running executable.
	served := call("/api/v1/agent/binary/linux/amd64")
	if served.Code != http.StatusOK || served.Body.String() != "signed-guard-binary" || served.Header().Get(agentBinaryChecksumHeader) != hex.EncodeToString(sum[:]) {
		t.Fatalf("binary status = %d body = %q", served.Code, served.Body.String())
	}

	// A manifest that names other bytes than the file is refused.
	signedArtifactMu.Lock()
	signedArtifactCache = map[string]agentBinaryArtifact{}
	signedArtifactMu.Unlock()
	writeSignedRelease(t, dir, "signed-guard-binary", hex.EncodeToString(make([]byte, 32)))
	if recorder := call("/api/v1/agent/release-evidence/linux/amd64"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("an inconsistent release answered %d", recorder.Code)
	}
}
