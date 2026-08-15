package local

import (
	"context"
	"testing"

	"github.com/kombifyio/go-common/authsession"
)

type memoryStore struct {
	record *Record
}

func (s *memoryStore) Get(context.Context) (*Record, error) {
	if s.record == nil {
		return nil, nil
	}
	clone := *s.record
	return &clone, nil
}

func (s *memoryStore) Save(_ context.Context, record *Record) error {
	clone := *record
	s.record = &clone
	return nil
}

func TestNewUsesTechStackBreakGlassEmail(t *testing.T) {
	manager, err := authsession.NewManager(authsession.Config{
		Audience: "techstack",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	store := &memoryStore{}
	service, err := New(Config{Store: store, Sessions: manager})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := service.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap returned error: %v", err)
	}
	if store.record == nil {
		t.Fatal("expected stored break-glass record")
	}
	if store.record.Email != BreakGlassEmail {
		t.Fatalf("Email = %q, want %q", store.record.Email, BreakGlassEmail)
	}
}
