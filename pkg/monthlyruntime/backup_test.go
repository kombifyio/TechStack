package monthlyruntime

import (
	"context"
	"fmt"
	"testing"
)

type fakeBackupChecker struct {
	enabled map[string]bool
	errs    map[string]error
}

func (f *fakeBackupChecker) IsEnabled(_ context.Context, key, _ string) (bool, error) {
	if err, ok := f.errs[key]; ok {
		return false, err
	}
	return f.enabled[key], nil
}

func TestEvaluateBackupEntitlementFailClosed(t *testing.T) {
	ctx := context.Background()

	// nil checker denies.
	decision := EvaluateBackupEntitlement(ctx, nil, "user-1", false)
	if !decision.Denied || decision.ReasonCode != EntitlementReasonCheckerUnavailable {
		t.Fatalf("nil checker: %+v", decision)
	}

	// check error denies.
	decision = EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		errs: map[string]error{FeatureBackupConfig: fmt.Errorf("edge down")},
	}, "user-1", false)
	if !decision.Denied || decision.ReasonCode != EntitlementReasonFeatureCheckFailed {
		t.Fatalf("check error: %+v", decision)
	}

	// disabled feature denies with the missing key.
	decision = EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		enabled: map[string]bool{FeatureBackupConfig: true},
	}, "user-1", true)
	if !decision.Denied || decision.MissingFeatures[0] != FeatureBackupContent {
		t.Fatalf("content denial: %+v", decision)
	}
}

func TestBackupDenialEnvelopeShape(t *testing.T) {
	decision := BackupEntitlementDecision{
		Denied:           true,
		ReasonCode:       EntitlementReasonFeatureDisabled,
		RequiredFeatures: []string{FeatureBackupConfig, FeatureBackupContent},
		MissingFeatures:  []string{FeatureBackupContent},
	}
	details := decision.BackupEntitlementDenialDetails()
	for _, key := range []string{"phase", "error_code", "reason_code", "capability", "required_features", "missing_features", "retryable", "user_guidance", "support_context"} {
		if _, ok := details[key]; !ok {
			t.Fatalf("denial envelope missing %q: %v", key, details)
		}
	}
	if details["error_code"] != BackupEntitlementErrorCode || details["retryable"] != false {
		t.Fatalf("envelope values: %v", details)
	}
}
