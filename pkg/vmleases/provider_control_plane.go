package vmleases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	controlPlaneDriverName = "pgx"
	controlPlaneQueryTTL   = 5 * time.Second
)

var providerCatalogIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

var (
	ErrProviderControlPlaneUnavailable = errors.New("vmleases: provider control plane unavailable")
	ErrNoTechStackLeasingProviders     = errors.New("vmleases: no active techstack_leasing providers configured")
)

func ResolveTechStackLeasingProviderSet(ctx context.Context, databaseURL string) (map[string]bool, string, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, "", ErrProviderControlPlaneUnavailable
	}

	db, err := sql.Open(controlPlaneDriverName, databaseURL)
	if err != nil {
		return nil, "", fmt.Errorf("open provider control plane db: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Minute)

	queryCtx, cancel := context.WithTimeout(ctx, controlPlaneQueryTTL)
	defer cancel()

	providers, err := LoadTechStackLeasingProviderSet(queryCtx, db)
	if err != nil {
		return nil, "", err
	}
	if len(providers) == 0 {
		return nil, "", ErrNoTechStackLeasingProviders
	}
	return providers, "provider_catalog", nil
}

func LoadTechStackLeasingProviderSet(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	if db == nil {
		return nil, ErrProviderControlPlaneUnavailable
	}

	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT profile.provider_id
FROM provider_catalog_versions AS version
JOIN provider_catalog_profiles AS profile
  ON profile.catalog_version = version.catalog_version
WHERE version.status = 'active'
ORDER BY profile.provider_id`)
	if err != nil {
		return nil, fmt.Errorf("query provider control plane techstack_leasing exposures: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var providerID string
		if err := rows.Scan(&providerID); err != nil {
			return nil, fmt.Errorf("scan provider control plane techstack_leasing provider: %w", err)
		}
		providerID = strings.TrimSpace(providerID)
		if !providerCatalogIDPattern.MatchString(providerID) ||
			providerID == "centron-managed" || providerID == "ionos-managed" {
			return nil, fmt.Errorf("invalid canonical provider catalog id %q", providerID)
		}
		out[providerID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read provider control plane techstack_leasing exposures: %w", err)
	}
	return out, nil
}

func IsProviderControlPlaneUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProviderControlPlaneUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"relation \"provider_catalog_versions\" does not exist",
		"relation \"provider_catalog_profiles\" does not exist",
		"undefined_table",
		"no such table",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
