package actions

import (
	"errors"
	"testing"
	"time"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestRefuseUnentitledStartDeniesMissingAndExpiredGrants(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	input := BeginExecution{Now: now}
	if err := RefuseUnentitledStart(&GovernedCard{Status: string(StatusApproved)}, input); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("missing grant = %v, want ErrGrantRequired", err)
	}
	expired := &GovernedCard{Template: ActionTemplate{Grant: &rilaction.GrantBinding{
		BindingRef: "grant:1", ValidUntil: now.Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	if err := RefuseUnentitledStart(expired, input); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("expired grant = %v, want ErrGrantRequired", err)
	}
	live := &GovernedCard{Template: ActionTemplate{Grant: &rilaction.GrantBinding{
		BindingRef: "grant:1", BindingHash: "sha256:abc", Audience: "stackkits",
		Scopes: []string{"stackkit-verify"}, GrantedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano),
	}}}
	if err := RefuseUnentitledStart(live, input); err != nil {
		t.Fatalf("live grant denied: %v", err)
	}
}
