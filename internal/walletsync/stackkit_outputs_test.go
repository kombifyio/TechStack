package walletsync

import (
	"context"
	"os"
	"testing"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestMain(m *testing.M) {
	// Wallet secret custody is fail-closed without a configured key; the
	// package harness supplies one before the global encryptor initializes.
	const testKey = "walletsync-test-key-exactly-32b!"
	if len(testKey) != 32 {
		panic("wallet encryption test key must be exactly 32 bytes")
	}
	os.Setenv(auth.EncryptionKeyEnvVar, testKey)
	os.Exit(m.Run())
}

func TestWalletItemsFromResultParsesStackKitIdentityOutputs(t *testing.T) {
	items := WalletItemsFromResult(SyncRequest{
		OwnerID:   "owner-1",
		StackID:   "stack-1",
		StackName: "Demo Stack",
		Result: map[string]interface{}{
			"stackkit_outputs": map[string]interface{}{
				"identity": map[string]interface{}{
					"owner": map[string]interface{}{
						"email":      "owner@example.com",
						"username":   "owner",
						"manage_url": "https://id.example.com/admin",
					},
					"login_gateway": map[string]interface{}{
						"url": "https://login.example.com",
					},
					"recovery": map[string]interface{}{
						"bundle_ref": "secret://stack/recovery-bundle",
					},
				},
			},
		},
	})

	if len(items) != 3 {
		t.Fatalf("items = %d, want 3: %+v", len(items), items)
	}
	if items[0].ServiceID != ownerWalletServiceID || items[0].ItemClass != "user_account" || items[0].AccessMode != "manage" {
		t.Fatalf("unexpected owner item: %+v", items[0])
	}
	if items[1].ServiceID != loginGatewayWalletServiceID || items[1].ItemClass != "launch" || items[1].AccessMode != "open" {
		t.Fatalf("unexpected login gateway item: %+v", items[1])
	}
	if items[2].ServiceID != recoveryWalletServiceID || items[2].ItemClass != "recovery" || items[2].Secret != "secret://stack/recovery-bundle" {
		t.Fatalf("unexpected recovery item: %+v", items[2])
	}
}

func TestSyncStackKitOutputsToStoreUpsertsWalletItemsIdempotently(t *testing.T) {
	store := controlplane.NewMemoryStore()
	req := SyncRequest{
		TenantID:  "tenant-1",
		OwnerID:   "owner-1",
		StackID:   "stack-1",
		StackName: "Demo Stack",
		Result: map[string]interface{}{
			"stackkit_outputs": map[string]interface{}{
				"identity": map[string]interface{}{
					"owner": map[string]interface{}{
						"email":      "owner@example.com",
						"manage_url": "https://id.example.com/admin",
					},
				},
				"login_gateway": map[string]interface{}{
					"url": "https://login.example.com",
				},
				"recovery": map[string]interface{}{
					"recovery_bundle_ref": "secret://stack/recovery-bundle",
				},
			},
		},
	}

	count, err := SyncStackKitOutputsToStore(context.Background(), store, req)
	if err != nil {
		t.Fatalf("SyncStackKitOutputsToStore() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("upsert count = %d, want 3", count)
	}
	count, err = SyncStackKitOutputsToStore(context.Background(), store, req)
	if err != nil {
		t.Fatalf("second SyncStackKitOutputsToStore() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("second upsert count = %d, want 3", count)
	}

	items, err := store.ListWalletItems(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListWalletItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("wallet items after second sync = %d, want idempotent 3", len(items))
	}
	recovery := findStoreWalletItemByService(items, recoveryWalletServiceID)
	if recovery == nil || recovery.Metadata["secret"] == "" || recovery.Metadata["revealable"] != true {
		t.Fatalf("recovery wallet item = %+v", recovery)
	}
	if secret, _ := recovery.Metadata["secret"].(string); !auth.IsEncrypted(secret) {
		t.Fatalf("recovery wallet secret must be stored encrypted, got %q", recovery.Metadata["secret"])
	}
	login := findStoreWalletItemByService(items, loginGatewayWalletServiceID)
	if login == nil || login.ExternalRef == "" || login.Metadata["url"] != "https://login.example.com" {
		t.Fatalf("login wallet item = %+v", login)
	}
}

func findStoreWalletItemByService(items []controlplane.WalletItem, serviceID string) *controlplane.WalletItem {
	for i := range items {
		if items[i].Metadata["service_id"] == serviceID {
			return &items[i]
		}
	}
	return nil
}
