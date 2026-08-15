package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/db"
)

type providerConfigJSON struct {
	ClientSecret     string      `json:"client_secret"`
	AuthorizationURL string      `json:"authorization_url"`
	TokenURL         string      `json:"token_url"`
	JWKSURL          string      `json:"jwks_url"`
	Scopes           interface{} `json:"scopes"`
}

// LoadRegistryFromDB loads enabled V2-compatible identity providers from the
// Postgres-backed identity_providers table.
//
// Non-V2 provider types such as pocketbase or api_key are ignored on purpose;
// they remain part of the legacy surface and must not break V2 startup.
func LoadRegistryFromDB(ctx context.Context, database *db.DB) (*Registry, error) {
	if database == nil || database.DB == nil {
		return nil, fmt.Errorf("%w: db handle is nil", ErrInvalidConfig)
	}
	rows, err := database.QueryContext(ctx, `
		SELECT provider_key, provider_type, issuer_url, client_id, audience, config_json
		FROM identity_providers
		WHERE enabled = true
		ORDER BY provider_key
	`)
	if err != nil {
		return nil, fmt.Errorf("load identity_providers: %w", err)
	}
	defer rows.Close()

	registry := NewRegistry()
	for rows.Next() {
		provider, supported, err := scanProviderRow(rows)
		if err != nil {
			return nil, err
		}
		if supported {
			registry.Add(provider)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate identity_providers: %w", err)
	}
	return registry, nil
}

func scanProviderRow(scanner interface {
	Scan(dest ...interface{}) error
}) (*Provider, bool, error) {
	var (
		providerKey  string
		providerType string
		issuerURL    sql.NullString
		clientID     sql.NullString
		audience     sql.NullString
		configJSON   []byte
	)
	if err := scanner.Scan(&providerKey, &providerType, &issuerURL, &clientID, &audience, &configJSON); err != nil {
		return nil, false, fmt.Errorf("scan identity_provider row: %w", err)
	}
	kind, supported := kindFromDBType(providerType)
	if !supported {
		return nil, false, nil
	}
	var extra providerConfigJSON
	if len(configJSON) > 0 && string(configJSON) != "null" {
		if err := json.Unmarshal(configJSON, &extra); err != nil {
			return nil, false, fmt.Errorf("decode config_json for provider %q: %w", providerKey, err)
		}
	}
	provider, err := New(Config{
		ID:               strings.TrimSpace(providerKey),
		Kind:             kind,
		Issuer:           strings.TrimSpace(issuerURL.String),
		Audience:         strings.TrimSpace(audience.String),
		ClientID:         strings.TrimSpace(clientID.String),
		ClientSecret:     strings.TrimSpace(extra.ClientSecret),
		AuthorizationURL: strings.TrimSpace(extra.AuthorizationURL),
		TokenURL:         strings.TrimSpace(extra.TokenURL),
		JWKSURL:          strings.TrimSpace(extra.JWKSURL),
		Scopes:           parseScopes(extra.Scopes),
	})
	if err != nil {
		return nil, false, fmt.Errorf("build provider %q: %w", providerKey, err)
	}
	return provider, true, nil
}

func kindFromDBType(raw string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auth0":
		return KindAuth0, true
	case "pocket_id":
		return KindPocketID, true
	case "generic_oidc":
		return KindGeneric, true
	default:
		return "", false
	}
}

func parseScopes(raw interface{}) []string {
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return strings.Fields(value)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if scope, ok := item.(string); ok && strings.TrimSpace(scope) != "" {
				out = append(out, strings.TrimSpace(scope))
			}
		}
		return out
	default:
		return nil
	}
}
