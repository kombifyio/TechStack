package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	updatemanifest "github.com/kombifyio/techstack/internal/gocommon/updateclient/manifest"
)

// The embedded key is the only trust anchor of the Windows update channel: a
// manifest signed by any other key, even under the production key id, must
// never stage an installer.
func TestClientUpdateRefusesManifestNotSignedByEmbeddedKey(t *testing.T) {
	_, foreignKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	installer := []byte("not the published installer")
	digest := sha256.Sum256(installer)
	mux := http.NewServeMux()
	var downloaded atomic.Bool
	var manifestFetched atomic.Bool
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	manifest := map[string]any{
		"schema_version": "1",
		"version":        "99.0.0",
		"channel":        "stable",
		"package_url":    server.URL + "/kombify-Techstack-Setup.exe",
		"package_sha256": hex.EncodeToString(digest[:]),
		"package_size":   len(installer),
		"published_at":   "2026-09-23T00:00:00Z",
	}
	payload := "kombify.windows-update.v1\n99.0.0\nstable\n" + manifest["package_url"].(string) + "\n" +
		manifest["package_sha256"].(string) + "\n" + strconv.Itoa(len(installer)) + "\n2026-09-23T00:00:00Z\n" + clientUpdateKeyID + "\n"
	manifest["signature"] = map[string]string{
		"algorithm": "Ed25519",
		"key_id":    clientUpdateKeyID,
		"value":     base64.RawURLEncoding.EncodeToString(ed25519.Sign(foreignKey, []byte(payload))),
	}
	mux.HandleFunc("/kombify-techstack-windows-update.json", func(w http.ResponseWriter, _ *http.Request) {
		manifestFetched.Store(true)
		_ = json.NewEncoder(w).Encode(manifest)
	})
	mux.HandleFunc("/kombify-Techstack-Setup.exe", func(w http.ResponseWriter, _ *http.Request) {
		downloaded.Store(true)
		_, _ = w.Write(installer)
	})

	staging := t.TempDir()
	var result clientUpdateResult
	err = stageClientUpdate(context.Background(), server.URL+"/kombify-techstack-windows-update.json", "stable",
		staging, "", time.Minute, server.Client(), &result)
	if err == nil {
		t.Fatalf("a manifest signed by a foreign key was accepted: %+v", result)
	}
	if _, statErr := os.Stat(filepath.Join(staging, "99.0.0")); !os.IsNotExist(statErr) {
		t.Fatalf("a foreign-signed update left staged files behind: %v", statErr)
	}
	// Recovery uses exactly the same embedded trust anchor. It must not
	// substitute an unsigned or foreign-signed prior release for a missing
	// Windows bundle cache, even though normal downgrade checks are not involved.
	err = stageInstalledClientRecovery(context.Background(), server.URL+"/kombify-techstack-windows-update.json", "stable",
		staging, version, time.Minute, server.Client(), &result)
	if !errors.Is(err, updatemanifest.ErrSignatureInvalid) {
		t.Fatalf("a foreign-signed recovery installer was accepted: %+v", result)
	}
	if downloaded.Load() {
		t.Fatal("a foreign-signed package was downloaded before trust verification")
	}
	err = stageClientRollback(context.Background(), server.URL+"/kombify-techstack-windows-update.json", "stable",
		staging, version, time.Minute, server.Client(), &result)
	if !errors.Is(err, updatemanifest.ErrSignatureInvalid) || downloaded.Load() {
		t.Fatalf("rollback staging bypassed the embedded key: %+v, %v", result, err)
	}
	manifestFetched.Store(false)
	err = stageClientRollback(context.Background(), server.URL+"/kombify-techstack-windows-update.json", "stable",
		staging, "99.0.0", time.Minute, server.Client(), &result)
	if err == nil || manifestFetched.Load() || downloaded.Load() {
		t.Fatal("rollback staging fetched a version newer than the running installation")
	}
}

func TestStagedInstallerHandoffRequiresSignedBoundPackage(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := updatemanifest.Keyring{Ed25519: map[string]ed25519.PublicKey{clientUpdateKeyID: publicKey}}
	for _, scenario := range []struct {
		name, expected, installed, purpose, channel, signedVersion string
		tamper, allowed                                            bool
	}{
		{name: "normal signed upgrade", expected: "99.0.0", installed: version, purpose: "update", channel: "stable", allowed: true},
		{name: "exact signed recovery", expected: version, installed: version, purpose: "recovery", channel: "stable", allowed: true},
		{name: "prior signed rollback", expected: version, installed: "99.0.0", purpose: "rollback", channel: "stable", allowed: true},
		{name: "foreign channel", expected: "99.0.0", installed: version, purpose: "update", channel: "beta"},
		{name: "foreign signed version", expected: "99.0.0", installed: version, purpose: "update", channel: "stable", signedVersion: "98.0.0"},
		{name: "recovery cannot change release", expected: "99.0.0", installed: version, purpose: "recovery", channel: "stable"},
		{name: "recovery requires exact metadata", expected: version + "+other", installed: version, purpose: "recovery", channel: "stable"},
		{name: "normal downgrade refused", expected: version, installed: "99.0.0", purpose: "update", channel: "stable"},
		{name: "tampered package and unsigned descriptor", expected: "99.0.0", installed: version, purpose: "update", channel: "stable", tamper: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, scenario.expected)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			packagePath := filepath.Join(directory, "package")
			original := []byte("signed release installer fixture")
			if err := os.WriteFile(packagePath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(original)
			signedVersion := scenario.signedVersion
			if signedVersion == "" {
				signedVersion = scenario.expected
			}
			m := &updatemanifest.Manifest{SchemaVersion: "1", Version: signedVersion, Channel: "stable",
				PackageURL:    "https://github.com/kombifyio/TechStack/releases/download/v" + signedVersion + "/kombify-Techstack-Setup.exe",
				PackageSHA256: hex.EncodeToString(digest[:]), PackageSize: int64(len(original)), PublishedAt: "2026-10-08T00:00:00Z",
				Signature: updatemanifest.Signature{Algorithm: "Ed25519", KeyID: clientUpdateKeyID}}
			payload := "kombify.windows-update.v1\n" + m.Version + "\n" + m.Channel + "\n" + m.PackageURL + "\n" +
				m.PackageSHA256 + "\n" + strconv.Itoa(len(original)) + "\n" + m.PublishedAt + "\n" + clientUpdateKeyID + "\n"
			m.Signature.Value = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(payload)))
			if err := preserveClientStageManifest(filepath.Join(directory, "stage.json"), m); err != nil {
				t.Fatal(err)
			}
			if scenario.tamper {
				changed := []byte("substituted elevation payload")
				if err := os.WriteFile(packagePath, changed, 0o600); err != nil {
					t.Fatal(err)
				}
				changedHash := sha256.Sum256(changed)
				unsigned, _ := json.Marshal(map[string]any{"version": scenario.expected, "channel": "stable",
					"package_path": packagePath, "package_sha256": hex.EncodeToString(changedHash[:]), "package_size": len(changed)})
				if err := os.WriteFile(filepath.Join(directory, "stage.json"), unsigned, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var result clientUpdateResult
			err := verifyStagedClientPackage(root, scenario.expected, scenario.channel, scenario.installed, scenario.purpose, keys, &result)
			if scenario.allowed {
				if err != nil {
					t.Fatal(err)
				}
				bytes, err := os.ReadFile(result.PackagePath)
				if err != nil || string(bytes) != string(original) || result.PackageSHA256 != m.PackageSHA256 || result.PackageSize != m.PackageSize {
					t.Fatalf("verified handoff changed its signer-bound installer: %+v, %v", result, err)
				}
			} else if err == nil || result.PackagePath != "" {
				t.Fatalf("an unbound installer reached handoff: %+v, %v", result, err)
			}
		})
	}
}
