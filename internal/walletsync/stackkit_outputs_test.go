package walletsync

import (
	"context"
	"fmt"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

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

func TestSyncStackKitOutputsUpsertsWalletItemsIdempotently(t *testing.T) {
	app := newFakeWalletApp()
	req := SyncRequest{
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

	count, err := SyncStackKitOutputs(app, req)
	if err != nil {
		t.Fatalf("SyncStackKitOutputs() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("upsert count = %d, want 3", count)
	}
	if len(app.records) != 3 {
		t.Fatalf("wallet records = %d, want 3", len(app.records))
	}

	count, err = SyncStackKitOutputs(app, req)
	if err != nil {
		t.Fatalf("second SyncStackKitOutputs() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("second upsert count = %d, want 3", count)
	}
	if len(app.records) != 3 {
		t.Fatalf("wallet records after second sync = %d, want idempotent 3", len(app.records))
	}

	owner := app.findByService(ownerWalletServiceID)
	if owner == nil || owner.GetString("item_class") != "user_account" || owner.GetString("access_mode") != "manage" {
		t.Fatalf("owner wallet item = %+v", owner)
	}
	login := app.findByService(loginGatewayWalletServiceID)
	if login == nil || login.GetString("url") != "https://login.example.com" || login.GetString("item_class") != "launch" {
		t.Fatalf("login wallet item = %+v", login)
	}
	recovery := app.findByService(recoveryWalletServiceID)
	if recovery == nil || recovery.GetString("secret") == "" || !recovery.GetBool("revealable") {
		t.Fatalf("recovery wallet item = %+v", recovery)
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
	login := findStoreWalletItemByService(items, loginGatewayWalletServiceID)
	if login == nil || login.ExternalRef == "" || login.Metadata["url"] != "https://login.example.com" {
		t.Fatalf("login wallet item = %+v", login)
	}
}

type fakeWalletApp struct {
	collection *core.Collection
	records    []*core.Record
	nextID     int
}

func newFakeWalletApp() *fakeWalletApp {
	collection := core.NewBaseCollection("wallet")
	collection.Fields.Add(
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "service_id"},
		&core.TextField{Name: "source_ref"},
		&core.TextField{Name: "name"},
		&core.SelectField{Name: "kind", Values: []string{"other"}},
		&core.TextField{Name: "username"},
		&core.TextField{Name: "secret", Max: 2000},
		&core.TextField{Name: "url", Max: 2000},
		&core.TextField{Name: "notes", Max: 5000},
		&core.SelectField{Name: "item_class", Values: []string{"launch", "user_account", "recovery"}},
		&core.TextField{Name: "source_type"},
		&core.SelectField{Name: "access_mode", Values: []string{"open", "reveal", "manage"}},
		&core.BoolField{Name: "revealable"},
		&core.BoolField{Name: "auto_generated"},
	)
	return &fakeWalletApp{collection: collection}
}

func (f *fakeWalletApp) FindCollectionByNameOrId(nameOrId string) (*core.Collection, error) {
	if nameOrId == "wallet" || nameOrId == f.collection.Id {
		return f.collection, nil
	}
	return nil, fmt.Errorf("collection not found")
}

func (f *fakeWalletApp) FindRecordsByFilter(_ any, _ string, _ string, limit int, _ int, params ...dbx.Params) ([]*core.Record, error) {
	if len(params) == 0 {
		return nil, nil
	}
	p := params[0]
	matches := []*core.Record{}
	for _, record := range f.records {
		if record.GetString("owner_id") == p["ownerID"] &&
			record.GetString("stack_id") == p["stackID"] &&
			record.GetString("service_id") == p["serviceID"] &&
			record.GetString("source_ref") == p["sourceRef"] {
			matches = append(matches, record)
			if limit > 0 && len(matches) >= limit {
				break
			}
		}
	}
	return matches, nil
}

func (f *fakeWalletApp) Save(model core.Model) error {
	record, ok := model.(*core.Record)
	if !ok {
		return nil
	}
	if record.Id == "" {
		f.nextID++
		record.Id = fmt.Sprintf("wallet-%d", f.nextID)
		f.records = append(f.records, record)
		return nil
	}
	for i, existing := range f.records {
		if existing.Id == record.Id {
			f.records[i] = record
			return nil
		}
	}
	f.records = append(f.records, record)
	return nil
}

func (f *fakeWalletApp) findByService(serviceID string) *core.Record {
	for _, record := range f.records {
		if record.GetString("service_id") == serviceID {
			return record
		}
	}
	return nil
}

func findStoreWalletItemByService(items []controlplane.WalletItem, serviceID string) *controlplane.WalletItem {
	for i := range items {
		if items[i].Metadata["service_id"] == serviceID {
			return &items[i]
		}
	}
	return nil
}
