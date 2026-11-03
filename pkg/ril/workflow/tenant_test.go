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

func TestWorkflowIdentityRejectsChangedConnectorProjection(t *testing.T) {
	original := &Run{RunID: "run", TenantID: "tenant", Type: TypeActionCardRemediation, OwnerID: "owner", ServerID: "server", CardID: "card", Input: map[string]any{"connector_projection": map[string]any{"binding_id": "binding-one"}}}
	changed := *original
	changed.Input = map[string]any{"connector_projection": map[string]any{"binding_id": "binding-two"}}
	if sameRunIdentity(original, &changed) {
		t.Fatal("changed projection adopted an existing workflow")
	}
	missing := *original
	missing.Input = map[string]any{}
	if sameRunIdentity(original, &missing) {
		t.Fatal("missing projection adopted an existing workflow")
	}
	identical := *original
	if !sameRunIdentity(original, &identical) {
		t.Fatal("identical input could not resume its workflow")
	}
}
