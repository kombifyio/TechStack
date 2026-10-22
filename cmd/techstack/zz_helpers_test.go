package main

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"context"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"strings"
)

type memoryBreakglassStore struct {
	rec *commonauthlocal.Record
	err error
}

func (s *memoryBreakglassStore) Get(context.Context) (*commonauthlocal.Record, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.rec == nil {
		return nil, nil
	}
	rec := *s.rec
	return &rec, nil
}

func (s *memoryBreakglassStore) Save(_ context.Context, rec *commonauthlocal.Record) error {
	if s.err != nil {
		return s.err
	}
	copy := *rec
	s.rec = &copy
	return nil
}

func (s *memoryBreakglassStore) CompareAndSwap(ctx context.Context, expected, next *commonauthlocal.Record) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if (s.rec == nil) != (expected == nil) || s.rec != nil && (*s.rec != *expected || s.rec.Claimed || s.rec.Locked) {
		return false, nil
	}
	return true, s.Save(ctx, next)
}

// stubAuthStore is an in-test controlplane.AuthStore double that records the
// upserts performed during cloud login.
type stubAuthStore struct {
	tenants     []controlplane.Tenant
	users       []controlplane.User
	memberships []controlplane.Membership
	membership  *controlplane.Membership
	calls       []string
}

func (s *stubAuthStore) UpsertTenant(_ context.Context, t controlplane.Tenant) (*controlplane.Tenant, error) {
	s.calls = append(s.calls, "tenant")
	s.tenants = append(s.tenants, t)
	return &t, nil
}

func (s *stubAuthStore) UpsertUser(_ context.Context, u controlplane.User) (*controlplane.User, error) {
	s.calls = append(s.calls, "user")
	s.users = append(s.users, u)
	return &u, nil
}

func (s *stubAuthStore) UpsertMembership(_ context.Context, m controlplane.Membership) (*controlplane.Membership, error) {
	s.calls = append(s.calls, "membership")
	s.memberships = append(s.memberships, m)
	stored := m
	s.membership = &stored
	return &m, nil
}

func (s *stubAuthStore) GetMembership(_ context.Context, tenantID, userID string) (*controlplane.Membership, error) {
	if s.membership == nil || s.membership.TenantID != tenantID || s.membership.UserID != userID {
		return nil, controlplane.ErrNotFound
	}
	stored := *s.membership
	return &stored, nil
}

func (s *stubAuthStore) ListMembershipsByUser(_ context.Context, userID string) ([]controlplane.Membership, error) {
	var out []controlplane.Membership
	for _, m := range s.memberships {
		if m.UserID == userID && (m.Status == "" || strings.EqualFold(m.Status, "active")) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *stubAuthStore) UpsertAuthConfig(_ context.Context, c controlplane.AuthConfig) (*controlplane.AuthConfig, error) {
	return &c, nil
}

func (s *stubAuthStore) UpsertBreakglassAdmin(_ context.Context, a controlplane.BreakglassAdmin) (*controlplane.BreakglassAdmin, error) {
	return &a, nil
}

func (s *stubAuthStore) GetBreakglassAdmin(_ context.Context, _ string) (*controlplane.BreakglassAdmin, error) {
	return nil, controlplane.ErrNotFound
}

