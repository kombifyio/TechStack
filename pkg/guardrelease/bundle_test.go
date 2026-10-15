package guardrelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTree(t *testing.T, files map[string]string, modes map[string]os.FileMode) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		mode := os.FileMode(0o644)
		if m, ok := modes[name]; ok {
			mode = m
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func archiveOf(t *testing.T, files map[string]string, modes map[string]os.FileMode) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	if err := writer.WriteHeader(&tar.Header{Name: ".stackkit/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if m, ok := modes[name]; ok {
			mode = m
		}
		if err := writer.WriteHeader(&tar.Header{Name: ".stackkit/" + name, Typeflag: tar.TypeReg, Mode: int64(mode), Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// The digest the release workflow signs over the bundle tree, the digest of the
// archive the control plane serves and the digest the privileged executor
// computes over the extracted tree are one value, and any change to any file
// (content, executable bit, an added or missing file) moves it.
func TestRuntimeTreeDigestIsTheSameForTreeAndArchiveAndMovesWithAnyFile(t *testing.T) {
	files := map[string]string{"bin/stackkit": "cli", "bin/stackkit-server": "server", "stackkits-advanced-operations-v1.json": `{"argv":["plan"]}`}
	modes := map[string]os.FileMode{"bin/stackkit": 0o755, "bin/stackkit-server": 0o750}
	want, err := TreeDigest(writeTree(t, files, modes))
	if err != nil {
		t.Fatal(err)
	}
	got, err := BundleTreeDigest(bytes.NewReader(archiveOf(t, files, modes)))
	if err != nil || got != want {
		t.Fatalf("archive digest %q (%v) differs from tree digest %q", got, err, want)
	}
	clone := func(mutate func(map[string]string, map[string]os.FileMode)) string {
		f, m := map[string]string{}, map[string]os.FileMode{}
		for k, v := range files {
			f[k] = v
		}
		for k, v := range modes {
			m[k] = v
		}
		mutate(f, m)
		digest, err := TreeDigest(writeTree(t, f, m))
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	for name, digest := range map[string]string{
		"forged catalog": clone(func(f map[string]string, _ map[string]os.FileMode) {
			f["stackkits-advanced-operations-v1.json"] = `{"argv":["rm"]}`
		}),
		"removed catalog": clone(func(f map[string]string, _ map[string]os.FileMode) {
			delete(f, "stackkits-advanced-operations-v1.json")
		}),
		"added file": clone(func(f map[string]string, _ map[string]os.FileMode) { f["bin/extra"] = "x" }),
		"executable catalog": clone(func(_ map[string]string, m map[string]os.FileMode) {
			m["stackkits-advanced-operations-v1.json"] = 0o755
		}),
	} {
		if digest == want {
			t.Errorf("%s did not change the tree digest", name)
		}
	}
}

// Refresh must retain the published runtime, even when the current compiler
// has different bytes. Only the archive paired with the signed tree is served.
func TestPublishedRuntimeBundleRemainsBoundAcrossCompilerChanges(t *testing.T) {
	published := map[string]string{"bin/stackkit": "published cli", "catalog.json": "published catalog"}
	modes := map[string]os.FileMode{"bin/stackkit": 0o755}
	digest, err := TreeDigest(writeTree(t, published, modes))
	if err != nil {
		t.Fatal(err)
	}
	runtime := StackKitsArchRuntime{BundleSHA256: digest}
	paired := archiveOf(t, published, modes)
	if err := VerifyRuntimeBundle(runtime, bytes.NewReader(paired)); err != nil {
		t.Fatalf("the published pair was refused: %v", err)
	}
	currentCompiler := archiveOf(t, map[string]string{"bin/stackkit": "new compiler", "catalog.json": "new catalog"}, modes)
	if err := VerifyRuntimeBundle(runtime, bytes.NewReader(currentCompiler)); err == nil {
		t.Fatal("the current compiler replaced the signed published runtime")
	}
	if err := VerifyRuntimeBundle(runtime, bytes.NewReader(paired)); err != nil {
		t.Fatalf("advancing the compiler invalidated the published pair: %v", err)
	}
	if err := VerifyRuntimeBundle(StackKitsArchRuntime{}, bytes.NewReader(paired)); err == nil {
		t.Fatal("an archive without a signed tree was admitted")
	}
	// Build-time refresh selects the already signed issuance window through
	// Options.Now. It still authenticates the publication; normal runtime
	// verification must continue to refuse its expired window.
	r := newRelease(t)
	raw := r.manifest(t, func(m *Manifest) {
		m.IssuedAt = r.now.Add(-72 * time.Hour)
		m.ExpiresAt = r.now.Add(-48 * time.Hour)
		m.StackKits = &StackKitsRuntime{Version: "v0.50.0", ArchiveSHA256: digestOf("archive"), BinarySHA256: digestOf("cli"), ServerSHA256: digestOf("server"), BundleSHA256: digest}
	}, "guard")
	entity, err := r.sigstore.Sign(testSigner, GitHubOIDCIssuer, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.verifier.verifyManifestEntity(raw, entity); err == nil {
		t.Fatal("ordinary verification admitted an expired manifest")
	}
	parsed, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	historical := *r.verifier
	historical.now = func() time.Time { return parsed.IssuedAt }
	verified, err := historical.verifyManifestEntity(raw, entity)
	if err != nil {
		t.Fatalf("authenticated historical publication could not refresh: %v", err)
	}
	path, _ := r.artifact(t, "guard")
	artifact, err := r.sigstore.Sign(testSigner, GitHubOIDCIssuer, []byte("guard"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := historical.verifyArtifactEntity(verified, "linux", "amd64", path, artifact, ""); err != nil {
		t.Fatalf("the original signed artifact could not refresh: %v", err)
	}
	identity, _ := verified.StackKits.ForArch("amd64")
	if err := VerifyRuntimeBundle(identity, bytes.NewReader(paired)); err != nil {
		t.Fatalf("the authenticated historical pair was refused: %v", err)
	}
	altered := bytes.Replace(raw, []byte(digest), []byte(digestOf("forged runtime")), 1)
	if _, err := historical.verifyManifestEntity(altered, entity); err == nil {
		t.Fatal("refresh admitted an altered signed publication")
	}
	if _, err := historical.VerifyManifest(raw, nil); err == nil {
		t.Fatal("refresh admitted unsigned publication metadata")
	}
}
