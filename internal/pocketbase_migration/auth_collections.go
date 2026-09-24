// Package pocketbase_migration contains bounded PocketBase compatibility code
// used while SaaS auth flows still bridge into legacy PocketBase users.
package pocketbase_migration

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// AuthConfigRecordID is the deterministic identity shared by every first-run
// producer. Existing installations with a legacy random ID are updated in place.
const AuthConfigRecordID = "authconfigroot0"

// EnsureSaaSAuthCollections creates the PocketBase collections required by
// Gateway-routed SaaS auth flows. It runs even when local user bootstrap is
// disabled in production because SSO/OIDC users are created on demand.
func EnsureSaaSAuthCollections(app core.App) error {
	if err := ensureUserLinksCollection(app); err != nil {
		return fmt.Errorf("ensure user_links collection: %w", err)
	}
	if err := ensureCloudLinkStatesCollection(app); err != nil {
		return fmt.Errorf("ensure cloud_link_states collection: %w", err)
	}
	return nil
}

// EnsureAuthConfigCollection creates the PocketBase singleton collection used
// only as the first-run auth marker. Provider configuration and secrets belong
// to environment custody, never PocketBase.
func EnsureAuthConfigCollection(app core.App) error {
	collection, err := app.FindCollectionByNameOrId("auth_config")
	if err != nil {
		collection = core.NewBaseCollection("auth_config")
	}
	for _, legacyField := range []string{
		"cloud_auth_url", "portal_url", "cloud_issuer", "cloud_client_id",
		"cloud_client_secret", "auth0_issuer", "auth0_client_id",
		"auth0_client_secret", "sso_jwt_secret",
	} {
		collection.Fields.RemoveByName(legacyField)
	}

	collection.Fields.Add(
		&core.TextField{Name: "mode", Required: true, Max: 32},
		&core.BoolField{Name: "allow_local_login"},
	)

	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save auth_config collection: %w", err)
	}
	return nil
}

// LoadAuthConfig returns the singleton first-run marker. Only an empty
// collection is reported as nil; schema and database failures propagate.
func LoadAuthConfig(app core.App) (*core.Record, error) {
	if app == nil {
		return nil, fmt.Errorf("auth config store is unavailable")
	}
	if err := EnsureAuthConfigCollection(app); err != nil {
		return nil, err
	}
	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

// UpsertAuthConfig is the single writer for the first-run marker.
func UpsertAuthConfig(app core.App, mode string, allowLocal bool) (*core.Record, error) {
	record, err := LoadAuthConfig(app)
	if err != nil {
		return nil, err
	}
	if record == nil {
		collection, err := app.FindCollectionByNameOrId("auth_config")
		if err != nil {
			return nil, err
		}
		record = core.NewRecord(collection)
		record.Id = AuthConfigRecordID
	}
	record.Set("mode", mode)
	record.Set("allow_local_login", allowLocal)
	if err := app.Save(record); err != nil {
		return nil, err
	}
	return record, nil
}

func ensureUserLinksCollection(app core.App) error {
	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return fmt.Errorf("find users collection: %w", err)
	}

	collection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		collection = core.NewBaseCollection("user_links")
	}
	collection.Fields.RemoveByName("is_admin")
	collection.Fields.RemoveByName("org_id")

	collection.Fields.Add(
		&core.RelationField{
			Name:          "user",
			Required:      true,
			CollectionId:  usersCollection.Id,
			CascadeDelete: true,
		},
		&core.SelectField{
			Name:     "provider",
			Required: true,
			Values:   []string{"cloud", "google", "github", "microsoft", "local"},
		},
		&core.TextField{Name: "external_id", Required: true, Max: 500},
		&core.TextField{Name: "external_email", Required: true, Max: 500},
		&core.TextField{Name: "external_name", Max: 200},
		// email_verified mirrors the provider claim so the cloud-linked owner
		// bootstrap can require a verified email without re-fetching userinfo.
		&core.BoolField{Name: "email_verified"},
	)

	ensureCollectionIndex(&collection.Indexes, "idx_user_links_user_provider",
		"CREATE UNIQUE INDEX idx_user_links_user_provider ON user_links (user, provider)")
	removeCollectionIndex(&collection.Indexes, "idx_user_links_external_id")
	ensureCollectionIndex(&collection.Indexes, "idx_user_links_provider_external_id",
		"CREATE UNIQUE INDEX idx_user_links_provider_external_id ON user_links (provider, external_id)")
	ensureCollectionIndex(&collection.Indexes, "idx_user_links_external_email",
		"CREATE INDEX idx_user_links_external_email ON user_links (external_email)")
	removeCollectionIndex(&collection.Indexes, "idx_user_links_org_id")

	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save user_links collection: %w", err)
	}
	return nil
}

// ensureCloudLinkStatesCollection creates the single-use PKCE state store for
// the cloud-link flow (connect a kombify Cloud profile to a local operator).
// expires_at/consumed_at are fixed-width RFC3339 UTC strings so lexicographic
// SQL comparison equals chronological comparison.
func ensureCloudLinkStatesCollection(app core.App) error {
	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return fmt.Errorf("find users collection: %w", err)
	}

	collection, err := app.FindCollectionByNameOrId("cloud_link_states")
	if err != nil {
		collection = core.NewBaseCollection("cloud_link_states")
	}

	collection.Fields.Add(
		&core.TextField{Name: "state_hash", Required: true, Max: 128},
		&core.RelationField{
			Name:          "user",
			Required:      true,
			CollectionId:  usersCollection.Id,
			CascadeDelete: true,
		},
		&core.TextField{Name: "code_verifier", Max: 1000},
		&core.TextField{Name: "purpose", Max: 100},
		&core.SelectField{
			Name:     "status",
			Required: true,
			Values:   []string{"issued", "consumed"},
		},
		&core.TextField{Name: "expires_at", Required: true, Max: 64},
		&core.TextField{Name: "consumed_at", Max: 64},
	)

	ensureCollectionIndex(&collection.Indexes, "idx_cloud_link_states_state_hash",
		"CREATE UNIQUE INDEX idx_cloud_link_states_state_hash ON cloud_link_states (state_hash)")
	ensureCollectionIndex(&collection.Indexes, "idx_cloud_link_states_user",
		"CREATE INDEX idx_cloud_link_states_user ON cloud_link_states (user)")

	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save cloud_link_states collection: %w", err)
	}
	return nil
}

func ensureCollectionIndex(indexes *types.JSONArray[string], name, statement string) {
	needle := strings.ToLower(name)
	for _, index := range *indexes {
		if strings.Contains(strings.ToLower(index), needle) {
			return
		}
	}
	*indexes = append(*indexes, statement)
}

func removeCollectionIndex(indexes *types.JSONArray[string], name string) {
	needle := strings.ToLower(name)
	filtered := (*indexes)[:0]
	for _, index := range *indexes {
		if !strings.Contains(strings.ToLower(index), needle) {
			filtered = append(filtered, index)
		}
	}
	*indexes = filtered
}
