package providercontrol

import (
	"context"
	"errors"
	"testing"
)

func TestStaticProviderCreatePolicyIsPerProviderAndFailClosed(t *testing.T) {
	policy := StaticProviderCreatePolicy{
		"ionos":   {Enabled: false, ReasonCode: ProviderCreateReasonSafetyIncomplete},
		"centron": {Enabled: true},
	}
	if err := policy.RequireProviderCreate(t.Context(), "centron"); err != nil {
		t.Fatalf("enabled provider: %v", err)
	}
	for _, providerID := range []string{"ionos", "unknown", ""} {
		err := policy.RequireProviderCreate(t.Context(), providerID)
		if !errors.Is(err, ErrProviderCreateBlocked) {
			t.Fatalf("provider %q error = %v, want ErrProviderCreateBlocked", providerID, err)
		}
		var blocked ProviderCreateBlockedError
		if !errors.As(err, &blocked) || blocked.ProviderID != providerID {
			t.Fatalf("provider %q blocked detail = %#v", providerID, blocked)
		}
	}
}

func TestStaticProviderCreatePolicyPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (StaticProviderCreatePolicy{"ionos": {Enabled: true}}).RequireProviderCreate(ctx, "ionos")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestStaticProviderCreatePolicyDoesNotExposeArbitraryReason(t *testing.T) {
	policy := StaticProviderCreatePolicy{"ionos": {ReasonCode: "secret operator detail"}}
	var blocked ProviderCreateBlockedError
	err := policy.RequireProviderCreate(t.Context(), "ionos")
	if !errors.As(err, &blocked) || blocked.ReasonCode != ProviderCreateReasonKillSwitchDisabled {
		t.Fatalf("blocked error exposed unbounded reason: %#v err=%v", blocked, err)
	}
}
