package featureflags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RuntimeRevision is the kombify-DB entitlement projection revision.
type RuntimeRevision struct {
	Revision         string
	PublishedAt      time.Time
	FeatureCount     int
	GrantCount       int
	UserSettingCount int
}

// RuntimeEntitlement is a row returned by get_entitlements from kombify-DB.
type RuntimeEntitlement struct {
	Revision         string
	Tool             string
	FeatureKey       string
	GrantKind        string
	SubjectKind      string
	SubjectKey       string
	Enabled          bool
	EffectiveEnabled bool
	TokenLimit       *int
	DefaultEnabled   bool
	UserConfigurable bool
	ReleasePhase     string
	LiveEnabled      bool
	Metadata         map[string]any
}

// RuntimeRevision fetches the current DB projection revision.
func (c *Client) RuntimeRevision(ctx context.Context) (RuntimeRevision, bool, error) {
	if c == nil || c.db == nil {
		return RuntimeRevision{}, false, fmt.Errorf("DB feature projection is not configured")
	}
	queryCtx, cancel := context.WithTimeout(ctx, c.cfg.DatabaseTimeout)
	defer cancel()

	row := c.db.QueryRow(
		queryCtx,
		`SELECT revision, published_at, feature_count, grant_count, user_setting_count FROM get_entitlement_revision($1)`,
		"feature_flags",
	)

	var revision RuntimeRevision
	if err := row.Scan(
		&revision.Revision,
		&revision.PublishedAt,
		&revision.FeatureCount,
		&revision.GrantCount,
		&revision.UserSettingCount,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RuntimeRevision{}, false, nil
		}
		return RuntimeRevision{}, false, err
	}
	return revision, true, nil
}

// GetEntitlements reads DB-projected entitlements for the supplied context.
func (c *Client) GetEntitlements(ctx context.Context, evalCtx EvaluationContext) ([]RuntimeEntitlement, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("DB feature projection is not configured")
	}
	queryCtx, cancel := context.WithTimeout(ctx, c.cfg.DatabaseTimeout)
	defer cancel()

	tx, err := c.db.Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("begin DB entitlement transaction: %w", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	for _, setting := range dbSessionSettings(c.cfg, evalCtx) {
		if _, settingErr := tx.Exec(queryCtx, `SELECT set_config($1, $2, true)`, setting.key, setting.value); settingErr != nil {
			return nil, fmt.Errorf("set DB entitlement context %s: %w", setting.key, settingErr)
		}
	}

	rows, err := tx.Query(
		queryCtx,
		`SELECT revision, tool, feature_key, grant_kind, subject_kind, subject_key, enabled,
		        effective_enabled, token_limit, default_enabled, user_configurable, release_phase,
		        live_enabled, metadata
		   FROM get_entitlements($1)`,
		nullableString(c.cfg.Category),
	)
	if err != nil {
		return nil, fmt.Errorf("query DB entitlements: %w", err)
	}
	defer rows.Close()

	var entitlements []RuntimeEntitlement
	for rows.Next() {
		var entitlement RuntimeEntitlement
		var metadata []byte
		if err := rows.Scan(
			&entitlement.Revision,
			&entitlement.Tool,
			&entitlement.FeatureKey,
			&entitlement.GrantKind,
			&entitlement.SubjectKind,
			&entitlement.SubjectKey,
			&entitlement.Enabled,
			&entitlement.EffectiveEnabled,
			&entitlement.TokenLimit,
			&entitlement.DefaultEnabled,
			&entitlement.UserConfigurable,
			&entitlement.ReleasePhase,
			&entitlement.LiveEnabled,
			&metadata,
		); err != nil {
			return nil, fmt.Errorf("scan DB entitlement row: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &entitlement.Metadata); err != nil {
				return nil, fmt.Errorf("decode DB entitlement metadata: %w", err)
			}
		}
		entitlements = append(entitlements, entitlement)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate DB entitlements: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("commit DB entitlement transaction: %w", err)
	}
	return entitlements, nil
}
