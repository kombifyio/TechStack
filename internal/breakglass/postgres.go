package breakglass

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/go-common/authlocal"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

const (
	breakglassMetadataClaimed      = "claimed"
	breakglassMetadataShowPassword = "show_password"
	breakglassMetadataShowUntil    = "show_until"
)

type PostgresStore struct {
	store    controlplane.AuthStore
	tenantID string
}

func NewPostgres(store controlplane.AuthStore, tenantID string) *PostgresStore {
	return &PostgresStore{
		store:    store,
		tenantID: strings.TrimSpace(tenantID),
	}
}

func (s *PostgresStore) Get(ctx context.Context) (*authlocal.Record, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("breakglass: nil auth store")
	}
	tenantID := s.tenantID
	if tenantID == "" {
		return nil, errors.New("breakglass: tenant id required")
	}

	admin, err := s.store.GetBreakglassAdmin(ctx, tenantID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return breakglassAdminToLocalRecord(admin), nil
}

func (s *PostgresStore) Save(ctx context.Context, r *authlocal.Record) error {
	if s == nil || s.store == nil {
		return errors.New("breakglass: nil auth store")
	}
	if r == nil {
		return errors.New("breakglass: nil record")
	}
	tenantID := s.tenantID
	if tenantID == "" {
		return errors.New("breakglass: tenant id required")
	}

	metadata := map[string]any{
		breakglassMetadataClaimed:      r.Claimed,
		breakglassMetadataShowPassword: r.ShowPassword,
	}
	if !r.ShowUntil.IsZero() {
		metadata[breakglassMetadataShowUntil] = r.ShowUntil.UTC().Format(time.RFC3339)
	}

	_, err := s.store.UpsertBreakglassAdmin(ctx, controlplane.BreakglassAdmin{
		ID:           authlocal.BreakGlassRecordID,
		TenantID:     tenantID,
		Email:        r.Email,
		PasswordHash: r.PasswordHash,
		Metadata:     metadata,
	})
	if err != nil {
		return fmt.Errorf("save breakglass admin: %w", err)
	}
	return nil
}

func breakglassAdminToLocalRecord(admin *controlplane.BreakglassAdmin) *authlocal.Record {
	if admin == nil {
		return nil
	}
	out := &authlocal.Record{
		Email:        admin.Email,
		PasswordHash: admin.PasswordHash,
		Claimed:      boolMetadata(admin.Metadata[breakglassMetadataClaimed]),
		ShowPassword: stringMetadata(admin.Metadata[breakglassMetadataShowPassword]),
		CreatedAt:    admin.CreatedAt,
		UpdatedAt:    admin.UpdatedAt,
	}
	if showUntil, ok := timeMetadata(admin.Metadata[breakglassMetadataShowUntil]); ok {
		out.ShowUntil = showUntil
	}
	return out
}

func boolMetadata(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func stringMetadata(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func timeMetadata(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), true
	case string:
		if strings.TrimSpace(typed) == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(typed))
		if err != nil {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	default:
		return time.Time{}, false
	}
}

var _ authlocal.Store = (*PostgresStore)(nil)
