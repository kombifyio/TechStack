package grpcserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryAgentEnrollmentStore_Get(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            "01",
		CertFingerprint:       "deadbeef",
		AllowedCommandClasses: []string{"health_check", "get_logs"},
		EnrolledBy:            "operator@example.com",
	})

	t.Run("happy path returns enrollment", func(t *testing.T) {
		got, err := store.Get(context.Background(), "tenant-A", "agent-1")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if got.AgentID != "agent-1" {
			t.Errorf("AgentID = %q, want agent-1", got.AgentID)
		}
		if got.TenantID != "tenant-A" {
			t.Errorf("TenantID = %q, want tenant-A", got.TenantID)
		}
		if got.CertSerial != "01" {
			t.Errorf("CertSerial = %q, want 01", got.CertSerial)
		}
		if !got.IsAllowed("health_check") {
			t.Errorf("IsAllowed(health_check) = false, want true")
		}
		if !got.IsAllowed("get_logs") {
			t.Errorf("IsAllowed(get_logs) = false, want true")
		}
		if got.IsAllowed("tofu") {
			t.Errorf("IsAllowed(tofu) = true, want false (not in allowlist)")
		}
		if got.EnrolledAt.IsZero() {
			t.Errorf("EnrolledAt should be auto-set, got zero")
		}
	})

	t.Run("missing returns ErrEnrollmentNotFound", func(t *testing.T) {
		_, err := store.Get(context.Background(), "tenant-A", "agent-99")
		if !errors.Is(err, ErrEnrollmentNotFound) {
			t.Errorf("err = %v, want ErrEnrollmentNotFound", err)
		}
	})

	t.Run("cross-tenant lookup is fail-closed", func(t *testing.T) {
		_, err := store.Get(context.Background(), "tenant-B", "agent-1")
		if !errors.Is(err, ErrEnrollmentNotFound) {
			t.Errorf("cross-tenant Get err = %v, want ErrEnrollmentNotFound", err)
		}
	})
}

func TestMemoryAgentEnrollmentStore_Revoke(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            "01",
		AllowedCommandClasses: []string{"health_check"},
	})

	t.Run("revoke marks RevokedAt", func(t *testing.T) {
		if err := store.Revoke("tenant-A", "agent-1"); err != nil {
			t.Fatalf("Revoke error: %v", err)
		}
		got, err := store.Get(context.Background(), "tenant-A", "agent-1")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if got.RevokedAt == nil || got.RevokedAt.IsZero() {
			t.Errorf("RevokedAt should be set, got %v", got.RevokedAt)
		}
		if got.RevokedAt.After(time.Now().Add(time.Minute)) {
			t.Errorf("RevokedAt is in the future: %v", got.RevokedAt)
		}
	})

	t.Run("revoking missing returns ErrEnrollmentNotFound", func(t *testing.T) {
		err := store.Revoke("tenant-A", "agent-missing")
		if !errors.Is(err, ErrEnrollmentNotFound) {
			t.Errorf("err = %v, want ErrEnrollmentNotFound", err)
		}
	})
}

func TestMemoryAgentEnrollmentStore_GetReturnsCopy(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		AllowedCommandClasses: []string{"health_check"},
	})

	got, err := store.Get(context.Background(), "tenant-A", "agent-1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	got.AllowedCommandClasses[0] = "tofu" // mutate

	again, err := store.Get(context.Background(), "tenant-A", "agent-1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if again.AllowedCommandClasses[0] != "health_check" {
		t.Errorf("internal state mutated by caller: %q", again.AllowedCommandClasses[0])
	}
}

func TestAgentEnrollment_IsAllowed_NilReceiver(t *testing.T) {
	var e *AgentEnrollment
	if e.IsAllowed("anything") {
		t.Errorf("nil receiver should never allow commands")
	}
}

func TestAgentEnrollment_IsAllowed_EmptyAllowlist(t *testing.T) {
	e := &AgentEnrollment{AllowedCommandClasses: nil}
	if e.IsAllowed("health_check") {
		t.Errorf("empty allowlist must reject (zero-trust default)")
	}
}
