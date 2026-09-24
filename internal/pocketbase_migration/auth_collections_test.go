package pocketbase_migration

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestEnsureSaaSAuthCollectionsRejectsDuplicateProviderSubject(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	if err := EnsureSaaSAuthCollections(app); err != nil {
		t.Fatal(err)
	}
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	links, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		t.Fatal(err)
	}
	for i, email := range []string{"first@example.test", "second@example.test"} {
		user := core.NewRecord(users)
		user.SetEmail(email)
		user.SetPassword("test-password-123456789")
		if err := app.Save(user); err != nil {
			t.Fatal(err)
		}
		link := core.NewRecord(links)
		link.Set("user", user.Id)
		link.Set("provider", "cloud")
		link.Set("external_id", "cloud|singular-subject")
		link.Set("external_email", email)
		err = app.Save(link)
		if i == 0 && err != nil {
			t.Fatal(err)
		}
	}
	if err == nil {
		t.Fatal("second profile claimed an already linked Cloud subject")
	}
}

func TestEnsureAuthConfigCollectionRemovesLegacySecretCustody(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	collection := core.NewBaseCollection("auth_config")
	collection.Fields.Add(
		&core.TextField{Name: "mode", Required: true, Max: 32},
		&core.TextField{Name: "sso_jwt_secret", Max: 1000},
	)
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("mode", "local")
	record.Set("sso_jwt_secret", "must-not-survive")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	if err := EnsureAuthConfigCollection(app); err != nil {
		t.Fatal(err)
	}
	reloaded, err := app.FindRecordById("auth_config", record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if secret := reloaded.GetString("sso_jwt_secret"); secret != "" {
		t.Fatal("legacy PocketBase secret survived custody migration")
	}
}
