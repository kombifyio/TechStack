package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/logger"
)

// trustsBundle accepts a manifest whose bundle says it is signed; it stands in
// for the pinned Sigstore verification, which is tested in pkg/guardrelease.
type trustsBundle struct{}

func (trustsBundle) VerifyManifest(raw, bundle []byte) (*guardrelease.Manifest, error) {
	if string(bundle) != `{"signed":true}` {
		return nil, errors.New("signature_invalid")
	}
	return guardrelease.ParseManifest(raw)
}

// The control plane serves a newer manifest only after it verified it, only when
// it describes the binaries this control plane serves, and never one that would
// weaken the policy it already serves. A failed or refused refresh leaves the
// served manifest unchanged.
func TestRefresherServesOnlyAVerifiedNewerManifestThatDescribesWhatItServes(t *testing.T) {
	refreshedGuardManifest.Lock()
	refreshedGuardManifest.value = nil
	refreshedGuardManifest.Unlock()
	t.Cleanup(func() {
		refreshedGuardManifest.Lock()
		refreshedGuardManifest.value = nil
		refreshedGuardManifest.Unlock()
	})
	signedArtifactMu.Lock()
	signedArtifactCache = map[string]agentBinaryArtifact{}
	signedArtifactMu.Unlock()

	dir := t.TempDir()
	sum := sha256.Sum256([]byte("signed-guard-binary"))
	digest := hex.EncodeToString(sum[:])
	writeSignedRelease(t, dir, "signed-guard-binary", digest) // seed: 0.50.0 issued an hour ago

	now := time.Now().UTC()
	var published map[string][]byte
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/releases/tags/guard-v0.50.0" {
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]any{
				{"id": 1, "name": guardReleaseManifestFile}, {"id": 2, "name": guardReleaseManifestSig},
			}})
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/assets/1":
			_, _ = w.Write(published[guardReleaseManifestFile])
		case "/repos/o/r/releases/assets/2":
			_, _ = w.Write(published[guardReleaseManifestSig])
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()
	refresher := &guardReleaseRefresher{
		dir: dir, repo: "o/r", apiBase: github.URL, client: github.Client(), verifier: trustsBundle{},
		now: func() time.Time { return now }, log: logger.NewNop(),
	}
	publish := func(bundle string, mutate func(*guardrelease.Manifest)) {
		manifest := guardrelease.Manifest{
			SchemaVersion: guardrelease.ManifestSchema, Version: "0.50.0", MinimumAdmittedVersion: "0.48.0",
			Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digest, Size: int64(len("signed-guard-binary"))}},
			IssuedAt:  now.Add(-time.Minute), ExpiresAt: now.Add(13 * 24 * time.Hour),
		}
		if mutate != nil {
			mutate(&manifest)
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		published = map[string][]byte{guardReleaseManifestFile: raw, guardReleaseManifestSig: []byte(bundle)}
	}
	served := func() *guardrelease.Manifest {
		t.Helper()
		current, _, err := currentGuardManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		return current.manifest
	}
	seedIssuedAt := served().IssuedAt

	publish(`{"signed":false}`, nil)
	if updated, err := refresher.refreshOnce(context.Background()); updated || err == nil {
		t.Fatalf("an unverifiable manifest was accepted: %v %v", updated, err)
	}
	publish(`{"signed":true}`, func(m *guardrelease.Manifest) { m.Artifacts[0].SHA256 = hex.EncodeToString(make([]byte, 32)) })
	if updated, err := refresher.refreshOnce(context.Background()); updated || err == nil {
		t.Fatalf("a manifest naming other binaries than the ones served was accepted: %v %v", updated, err)
	}
	if !served().IssuedAt.Equal(seedIssuedAt) {
		t.Fatal("a refused manifest changed the served manifest")
	}

	publish(`{"signed":true}`, func(m *guardrelease.Manifest) { m.RevokedVersions = []string{"0.49.5"} })
	if updated, err := refresher.refreshOnce(context.Background()); err != nil || !updated {
		t.Fatalf("a verified newer manifest was not served: %v %v", updated, err)
	}
	if got := served(); len(got.RevokedVersions) != 1 || !got.IssuedAt.After(seedIssuedAt) {
		t.Fatalf("served manifest = %+v", got)
	}

	// Older, or newer but silent about the revocation: the served manifest stays.
	publish(`{"signed":true}`, func(m *guardrelease.Manifest) { m.IssuedAt = now.Add(-2 * time.Minute) })
	if updated, err := refresher.refreshOnce(context.Background()); updated || err != nil {
		t.Fatalf("an older manifest replaced the served one: %v %v", updated, err)
	}
	publish(`{"signed":true}`, func(m *guardrelease.Manifest) { m.IssuedAt = now })
	if updated, err := refresher.refreshOnce(context.Background()); updated || err == nil {
		t.Fatalf("a manifest that drops a revocation was accepted: %v %v", updated, err)
	}
	if got := served(); len(got.RevokedVersions) != 1 {
		t.Fatalf("the revocation was lost: %+v", got)
	}
}
