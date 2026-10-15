package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

// Tenant omission must deny before a pool or workflow activity is reached.
// Positive PostgreSQL isolation is exercised by the native managed ril-scope flow.
func TestEngineRequiresTenantBeforeStoreAccess(t *testing.T) {
	e := NewEngine(NewPgStore(nil), NewActivityRunner())
	for _, ctx := range []context.Context{nil, context.Background(), WithTenant(t.Context(), " ")} {
		for _, operation := range []func() error{
			func() error { _, err := e.StartRun(ctx, &Run{}); return err },
			func() error { _, err := e.StartOrResumeRun(ctx, &Run{}); return err },
			func() error { return e.Advance(ctx, "run") },
			func() error { return e.Deliver(ctx, Signal{Key: "signal"}) },
			func() error { _, err := e.GetRun(ctx, "run"); return err },
			func() error { _, err := e.ListSteps(ctx, "run"); return err },
			func() error { _, err := e.ListAudit(ctx, "tenant", "run", 1); return err },
		} {
			if err := operation(); !errors.Is(err, db.ErrNoTenant) {
				t.Fatalf("unscoped workflow was not denied before store access: %v", err)
			}
		}
	}
}
