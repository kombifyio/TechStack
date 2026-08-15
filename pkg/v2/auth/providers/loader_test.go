package providers

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/kombifyio/techstack/pkg/db"
)

func TestLoadRegistryFromDBFiltersAndBuildsProviders(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	rows := sqlmock.NewRows([]string{"provider_key", "provider_type", "issuer_url", "client_id", "audience", "config_json"}).
		AddRow("auth0-primary", "auth0", "https://login.kombify.io", "client-1", "", []byte(`{"client_secret":"secret-1","scopes":["openid","profile","email"]}`)).
		AddRow("legacy-pocketbase", "pocketbase", nil, nil, nil, []byte(`{}`)).
		AddRow("oidc-generic", "generic_oidc", "https://id.example", "client-2", "aud-2", []byte(`{"authorization_url":"https://id.example/custom-auth","token_url":"https://id.example/custom-token","jwks_url":"https://id.example/keys","scopes":"openid profile email groups"}`))

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT provider_key, provider_type, issuer_url, client_id, audience, config_json
		FROM identity_providers
		WHERE enabled = true
		ORDER BY provider_key
	`)).WillReturnRows(rows)

	registry, err := LoadRegistryFromDB(context.Background(), &db.DB{DB: sqlDB})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 2 {
		t.Fatalf("len=%d", registry.Len())
	}
	auth0Provider, err := registry.Get("auth0-primary")
	if err != nil {
		t.Fatal(err)
	}
	if auth0Provider.Kind() != KindAuth0 {
		t.Fatalf("kind=%q", auth0Provider.Kind())
	}
	if auth0Provider.ClientSecret() != "secret-1" {
		t.Fatalf("client secret=%q", auth0Provider.ClientSecret())
	}
	genericProvider, err := registry.Get("oidc-generic")
	if err != nil {
		t.Fatal(err)
	}
	if genericProvider.AuthorizationURL() != "https://id.example/custom-auth" {
		t.Fatalf("authorization url=%q", genericProvider.AuthorizationURL())
	}
	if genericProvider.TokenURL() != "https://id.example/custom-token" {
		t.Fatalf("token url=%q", genericProvider.TokenURL())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRegistryFromDBRejectsInvalidConfig(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	rows := sqlmock.NewRows([]string{"provider_key", "provider_type", "issuer_url", "client_id", "audience", "config_json"}).
		AddRow("broken", "auth0", "", "", nil, []byte(`{}`))

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT provider_key, provider_type, issuer_url, client_id, audience, config_json
		FROM identity_providers
		WHERE enabled = true
		ORDER BY provider_key
	`)).WillReturnRows(rows)

	_, err = LoadRegistryFromDB(context.Background(), &db.DB{DB: sqlDB})
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRegistryFromDBNilHandle(t *testing.T) {
	_, err := LoadRegistryFromDB(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = LoadRegistryFromDB(context.Background(), &db.DB{DB: (*sql.DB)(nil)})
	if err == nil {
		t.Fatal("expected error for nil sql.DB")
	}
}
