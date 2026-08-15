package pocketbase_migration

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

func TestEnsureSaaSAuthCollectionsCreatesUserLinks(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if _, findErr := app.FindCollectionByNameOrId("user_links"); findErr == nil {
		t.Fatal("test fixture unexpectedly already has user_links collection")
	}

	if ensureErr := EnsureSaaSAuthCollections(app); ensureErr != nil {
		t.Fatalf("EnsureSaaSAuthCollections() error = %v", ensureErr)
	}

	collection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		t.Fatalf("find user_links collection: %v", err)
	}

	for _, field := range []string{
		"user",
		"provider",
		"external_id",
		"external_email",
		"external_name",
		"org_id",
		"is_admin",
	} {
		if collection.Fields.GetByName(field) == nil {
			t.Fatalf("user_links missing field %q", field)
		}
	}

	for _, index := range []string{
		"idx_user_links_user_provider",
		"idx_user_links_external_id",
		"idx_user_links_external_email",
		"idx_user_links_org_id",
	} {
		if !collectionHasIndex(collection.Indexes, index) {
			t.Fatalf("user_links missing index %q in %v", index, collection.Indexes)
		}
	}
}

func TestEnsureSaaSAuthCollectionsIsIdempotent(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if ensureErr := EnsureSaaSAuthCollections(app); ensureErr != nil {
		t.Fatalf("first EnsureSaaSAuthCollections() error = %v", ensureErr)
	}
	if ensureErr := EnsureSaaSAuthCollections(app); ensureErr != nil {
		t.Fatalf("second EnsureSaaSAuthCollections() error = %v", ensureErr)
	}

	collection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		t.Fatalf("find user_links collection: %v", err)
	}

	if got := countCollectionIndexes(collection.Indexes, "idx_user_links_external_id"); got != 1 {
		t.Fatalf("idx_user_links_external_id count = %d, want 1 in %v", got, collection.Indexes)
	}
	if got := countCollectionIndexes(collection.Indexes, "idx_user_links_org_id"); got != 1 {
		t.Fatalf("idx_user_links_org_id count = %d, want 1 in %v", got, collection.Indexes)
	}
}

func TestEnsureAuthConfigCollectionCreatesFirstRunConfigStore(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if _, findErr := app.FindCollectionByNameOrId("auth_config"); findErr == nil {
		t.Fatal("test fixture unexpectedly already has auth_config collection")
	}

	if ensureErr := EnsureAuthConfigCollection(app); ensureErr != nil {
		t.Fatalf("EnsureAuthConfigCollection() error = %v", ensureErr)
	}

	collection, err := app.FindCollectionByNameOrId("auth_config")
	if err != nil {
		t.Fatalf("find auth_config collection: %v", err)
	}

	for _, field := range []string{
		"mode",
		"allow_local_login",
		"cloud_auth_url",
		"portal_url",
		"cloud_issuer",
		"cloud_client_id",
		"cloud_client_secret",
		"sso_jwt_secret",
	} {
		if collection.Fields.GetByName(field) == nil {
			t.Fatalf("auth_config missing field %q", field)
		}
	}
}

func collectionHasIndex(indexes []string, name string) bool {
	return countCollectionIndexes(indexes, name) > 0
}

func countCollectionIndexes(indexes []string, name string) int {
	count := 0
	needle := strings.ToLower(name)
	for _, index := range indexes {
		if strings.Contains(strings.ToLower(index), needle) {
			count++
		}
	}
	return count
}
