package advancedissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"

	wire "github.com/kombifyio/stackkits/contracts/advancedcapability"
)

// Regression (platform-hm8lo): installations created before the StackKits
// issuer pin stored "techstack.<uuid>" and signed with it, so StackKits denied
// every Advanced operation (restore drill first) as
// advanced_capability_untrusted_key.
func TestLegacyInstallationSignsAsThePinnedIssuer(t *testing.T) {
	encryptor, err := auth.NewSecretEncryptor(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := encryptor.Encrypt(base64.RawStdEncoding.EncodeToString(privateKey.Seed()))
	if err != nil {
		t.Fatal(err)
	}
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const legacyLabel = "techstack.0471fa19-6a43-41b9-8508-6289c48e8551"
	for range 2 {
		mock.ExpectQuery("SELECT issuer_id, key_id, private_key_enc FROM advanced_issuer_keys").
			WillReturnRows(sqlmock.NewRows([]string{"issuer_id", "key_id", "private_key_enc"}).
				AddRow(legacyLabel, wire.KeyID(publicKey), encrypted))
	}

	store, err := NewPostgresStore(&db.DB{DB: database}, encryptor)
	if err != nil {
		t.Fatal(err)
	}
	// Governed preparation and Delivery attest the retained installation label.
	label, _, err := store.LoadExistingKey(context.Background())
	if err != nil || label != legacyLabel {
		t.Fatalf("LoadExistingKey() = %q, %v; want the retained label %q", label, err, legacyLabel)
	}
	issuer, err := openStore(context.Background(), store, nil)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	var bundle struct {
		Keys []struct {
			IssuerID string `json:"issuerId"`
			KeyID    string `json:"keyId"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(issuer.TrustBundle(), &bundle); err != nil {
		t.Fatal(err)
	}
	if issuer.IssuerID() != "techstack" || len(bundle.Keys) != 1 || bundle.Keys[0].IssuerID != "techstack" {
		t.Fatalf("legacy installation signs as %q with bundle %+v, want the pinned issuer techstack", issuer.IssuerID(), bundle.Keys)
	}
	if bundle.Keys[0].KeyID != wire.KeyID(publicKey) {
		t.Fatalf("bundle key %s, want the installation's unchanged key %s", bundle.Keys[0].KeyID, wire.KeyID(publicKey))
	}
}
