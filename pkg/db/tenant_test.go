package db

import (
	"context"
	"errors"
	"testing"
)

func TestWithTenantEmptyTenant(t *testing.T) {
	d := &DB{}
	if err := d.WithTenant(context.Background(), "", nil); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("empty: got %v, want ErrNoTenant", err)
	}
	if err := d.WithTenant(context.Background(), "   ", nil); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("whitespace: got %v, want ErrNoTenant", err)
	}
}

func TestWithTenantNilHandle(t *testing.T) {
	var d *DB
	if err := d.WithTenant(context.Background(), "tenant-1", nil); err == nil {
		t.Fatal("expected error for nil DB handle")
	}
}
