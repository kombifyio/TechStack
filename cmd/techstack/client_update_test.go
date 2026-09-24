package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
		_ = json.NewEncoder(w).Encode(manifest)
	})
	mux.HandleFunc("/kombify-Techstack-Setup.exe", func(w http.ResponseWriter, _ *http.Request) {
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
}
