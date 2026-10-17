package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

func TestRecipientKeyGenerationRequiresProtectedFile(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, args := range [][]string{
		{"recipient-key", "generate"},
		{"recipient-key", "generate", "--private-out", ""},
		{"recipient-key", "generate", "--private-out", " \t"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runWalletWith(args, &stdout, &stderr, getenv); err == nil {
			t.Fatal("generation without a protected file was accepted")
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("generation without a protected file emitted output")
		}
	}

	path := filepath.Join(t.TempDir(), "recipient.jwk")
	args := []string{"recipient-key", "generate", "--generation", "7", "--private-out", path}
	var stdout, stderr bytes.Buffer
	if err := runWalletWith(args, &stdout, &stderr, getenv); err != nil {
		t.Fatal("generation into a new protected file failed")
	}
	private, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot load the generated key through its protected sink")
	}
	defer clear(private)
	client := walletservice.New(walletservice.Config{
		Origin: kombifywallet.DefaultServiceURL, ServiceID: kombifywallet.ServiceID,
		ServiceSecret: "recipient-key-test", RecipientKey: strings.TrimSpace(string(private)), RecipientGeneration: "7",
	})
	if !client.Enabled() {
		t.Fatal("the protected key is unusable by the existing Wallet client")
	}
	var key struct {
		D string `json:"d"`
	}
	if json.Unmarshal(private, &key) != nil || key.D == "" {
		t.Fatal("the protected sink did not receive a private key")
	}
	if strings.Contains(stdout.String(), key.D) || strings.Contains(stderr.String(), key.D) {
		t.Fatal("private key material reached command output")
	}
	var enrollment map[string]struct {
		Generation string                  `json:"generation"`
		JWK        walletservice.PublicJWK `json:"jwk"`
	}
	var thumbprint, generation string
	for _, line := range strings.Split(stdout.String(), "\n") {
		name, value, _ := strings.Cut(line, "=")
		switch name {
		case "WALLET_SERVICE_RECIPIENTS":
			if json.Unmarshal([]byte(value), &enrollment) != nil {
				t.Fatal("public recipient enrollment is not usable JSON")
			}
		case "public_thumbprint":
			thumbprint = value
		case kombifywallet.EnvRecipientGeneration:
			generation = value
		}
	}
	public := client.RecipientPublicJWK()
	registered := enrollment[kombifywallet.ServiceID]
	if registered.Generation != "7" || generation != "7" || registered.JWK != public || thumbprint != public.Thumbprint() {
		t.Fatal("public enrollment does not bind the protected private key and requested generation")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("the private key file is not owner-only")
		}
	}
	stdout.Reset()
	stderr.Reset()
	if err := runWalletWith(args, &stdout, &stderr, getenv); err == nil {
		t.Fatal("generation overwrote an existing private key")
	}
	preserved, err := os.ReadFile(path)
	defer clear(preserved)
	if err != nil || !bytes.Equal(private, preserved) || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("refused overwrite changed the protected key or emitted output")
	}
}
