package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// RuntimeStore binds the existing PostgreSQL auth authority to the running
// instance. It carries no alternative authentication or database backend.
type RuntimeStore struct {
	Persistence controlplane.AuthRuntimeStore
	Identities  controlplane.AuthStore
	TenantID    string
	InstanceID  string
}

func (s RuntimeStore) Validate() error {
	if s.Persistence == nil || s.Identities == nil || strings.TrimSpace(s.TenantID) == "" || strings.TrimSpace(s.InstanceID) == "" {
		return fmt.Errorf("canonical PostgreSQL auth runtime binding is required")
	}
	return nil
}

func (s RuntimeStore) authConfig(ctx context.Context) (*controlplane.AuthConfig, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	value, err := s.Persistence.GetAuthConfig(ctx, s.TenantID, s.InstanceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, nil
	}
	return value, err
}

// ProjectLocalOwner is called only after the canonical local authority has
// accepted a claim, or from startup with its persisted claimed record.
func (s RuntimeStore) ProjectLocalOwner(ctx context.Context, name, email string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(email) == "" {
		return fmt.Errorf("claimed local owner email is required")
	}
	subject := "breakglass:" + authlocal.BreakGlassRecordID
	if _, err := s.Identities.UpsertUser(ctx, controlplane.User{
		ID: subject, PrimaryEmail: email, DisplayName: name,
	}); err != nil {
		return err
	}
	_, err := s.Identities.UpsertMembership(ctx, controlplane.Membership{
		ID: "local-owner:" + s.TenantID, TenantID: s.TenantID, UserID: subject,
		RoleKey: "admin", ProviderKey: "local", SubjectID: subject, Status: "active",
	})
	return err
}
