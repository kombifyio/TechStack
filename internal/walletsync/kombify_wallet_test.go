package walletsync

import (
	"context"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
)

func identityOutputs(recovery map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"stackkit_outputs": map[string]interface{}{
			"identity": map[string]interface{}{
				"owner": map[string]interface{}{
					"email": "alice@example.com", "display_name": "Alice Example", "manage_url": "https://id.example.com/admin",
				},
				"login_gateway": map[string]interface{}{"url": "https://login.example.com"},
				"recovery":      recovery,
			},
		},
	}
}

// The outputs of a rollout land in the owner's Wallet, in the stack's project,
// under names that carry no personal data, and a repeated sync converges.
func TestStackKitOutputsAreSavedInTheOwnersWalletWithoutPersonalDataInTheNames(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate("owner-1")
	wallet := fake.Client()
	request := SyncRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", StackName: "Alice's lab",
		Result: identityOutputs(map[string]interface{}{"bundle_ref": "secret://stack/recovery-bundle"}),
	}

	saved, err := SyncStackKitOutputsToKombifyWallet(context.Background(), wallet, request)
	if err != nil || saved != 3 {
		t.Fatalf("first sync: saved=%d err=%v", saved, err)
	}
	if _, err := SyncStackKitOutputsToKombifyWallet(context.Background(), wallet, request); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	items := fake.Items("owner-1")
	if len(items) != 3 {
		t.Fatalf("a repeated sync must converge on one item per output, got %d: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Scope != "project:stack-1" || len(item.Versions) != 1 {
			t.Fatalf("item %q: scope=%q versions=%d", item.DisplayName, item.Scope, len(item.Versions))
		}
		// A reference to a machine secret is not a secret of the owner: it is
		// configuration, which the Wallet can deliver back for the comparison.
		if item.DisplayName == "Stack recovery bundle" && item.Type != kombifywallet.ItemTypeConfiguration {
			t.Fatalf("the recovery reference was saved as %s", item.Type)
		}
		visible := item.DisplayName + " " + item.Name
		for _, personal := range []string{"Alice", "alice@example.com", "example.com"} {
			if strings.Contains(visible, personal) {
				t.Fatalf("personal data %q reached the visible name of %q", personal, item.DisplayName)
			}
		}
	}
}

// A real secret becomes a generic secret that carries only that secret, and a
// rotated value is a new version of the same item.
func TestStackKitRecoverySecretIsAGenericSecretAndRotationAddsAVersion(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate("owner-1")
	wallet := fake.Client()
	request := SyncRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		Result: identityOutputs(map[string]interface{}{"passphrase_hash": "$argon2id$first"}),
	}
	if _, err := SyncStackKitOutputsToKombifyWallet(context.Background(), wallet, request); err != nil {
		t.Fatal(err)
	}
	request.Result = identityOutputs(map[string]interface{}{"passphrase_hash": "$argon2id$rotated"})
	if _, err := SyncStackKitOutputsToKombifyWallet(context.Background(), wallet, request); err != nil {
		t.Fatal(err)
	}
	var secrets int
	for _, item := range fake.Items("owner-1") {
		if item.Type != kombifywallet.ItemTypeGenericSecret {
			continue
		}
		secrets++
		if string(item.Value()) != "$argon2id$rotated" || len(item.Versions) != 2 {
			t.Fatalf("recovery secret: value=%q versions=%d", item.Value(), len(item.Versions))
		}
	}
	if secrets != 1 {
		t.Fatalf("expected exactly one generic secret, got %d", secrets)
	}
}

// Without the owner's create right nothing is saved and the denial says so.
func TestStackKitOutputsAreNotSavedWithoutTheCreateRight(t *testing.T) {
	fake := walletfake.New(t)
	_, err := SyncStackKitOutputsToKombifyWallet(context.Background(), fake.Client(), SyncRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		Result: identityOutputs(map[string]interface{}{"bundle_ref": "secret://stack/recovery-bundle"}),
	})
	denial, ok := kombifywallet.AsDenial(err)
	if !ok || denial.Reason != kombifywallet.ReasonCreateRightMissing {
		t.Fatalf("err = %v, want a create-right denial", err)
	}
	if items := fake.Items("owner-1"); len(items) != 0 {
		t.Fatalf("items were saved without the right: %+v", items)
	}
}
