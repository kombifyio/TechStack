package monthlyruntime

import (
	"context"
	"fmt"
	"strings"
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

func TestEvaluateBackupEntitlementQuotaTiers(t *testing.T) {
	ctx := context.Background()

	pro := EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		enabled: map[string]bool{
			FeatureBackupConfig:  true,
			FeatureBackupContent: true,
		},
	}, "user-1", true)
	if pro.Denied || pro.QuotaBytes != int64(250)<<30 {
		t.Fatalf("pro quota: %+v", pro)
	}

	ayn := EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		enabled: map[string]bool{
			FeatureBackupConfig:     true,
			FeatureBackupContent:    true,
			FeatureBackupStorageAyn: true,
		},
	}, "user-1", true)
	if ayn.Denied || ayn.QuotaBytes != int64(1)<<40 {
		t.Fatalf("ayn quota: %+v", ayn)
	}
}

func TestBackupQuotaEnvOverrides(t *testing.T) {
	ctx := context.Background()
	checker := &fakeBackupChecker{enabled: map[string]bool{
		FeatureBackupConfig:     true,
		FeatureBackupContent:    true,
		FeatureBackupStorageAyn: true,
	}}

	t.Setenv("TECHSTACK_BACKUP_QUOTA_AYN_GB", "2048")
	if got := EvaluateBackupEntitlement(ctx, checker, "user-1", true).QuotaBytes; got != int64(2048)<<30 {
		t.Fatalf("ayn override: %d", got)
	}

	t.Setenv("TECHSTACK_BACKUP_QUOTA_AYN_GB", "not-a-number")
	if got := EvaluateBackupEntitlement(ctx, checker, "user-1", true).QuotaBytes; got != int64(1)<<40 {
		t.Fatalf("invalid override must fall back: %d", got)
	}

	proChecker := &fakeBackupChecker{enabled: map[string]bool{FeatureBackupConfig: true}}
	t.Setenv("TECHSTACK_BACKUP_QUOTA_PRO_GB", "10")
	if got := EvaluateBackupEntitlement(ctx, proChecker, "user-1", false).QuotaBytes; got != int64(10)<<30 {
		t.Fatalf("pro override: %d", got)
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

	quota := BackupQuotaExceededDetails(int64(250)<<30, int64(260)<<30)
	if quota["error_code"] != BackupQuotaExceededErrorCode || quota["reason_code"] != ReasonQuotaExceeded {
		t.Fatalf("quota envelope: %v", quota)
	}
	guidance := quota["user_guidance"].(map[string]any)
	if body := guidance["body"].(string); !strings.Contains(body, "260.0 GB") || !strings.Contains(body, "250.0 GB") {
		t.Fatalf("quota guidance body: %s", body)
	}
}
