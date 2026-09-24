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

	// An account holding a storage key gets exactly that tier's budget.
	for _, testCase := range []struct {
		name       string
		storageKey string
		wantBytes  int64
	}{
		{"starter", FeatureBackupStorageStarter, int64(50) << 30},
		{"pro", FeatureBackupStoragePro, int64(250) << 30},
		{"ayn", FeatureBackupStorageAyn, int64(500) << 30},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			decision := EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
				enabled: map[string]bool{
					FeatureBackupConfig:  true,
					FeatureBackupContent: true,
					testCase.storageKey:  true,
				},
			}, "user-1", true)
			if decision.Denied || decision.QuotaBytes != testCase.wantBytes {
				t.Fatalf("quota: %+v", decision)
			}
			if decision.GrantedStorageFeature != testCase.storageKey {
				t.Fatalf("granted key = %q, want %q", decision.GrantedStorageFeature, testCase.storageKey)
			}
		})
	}

	// The largest held tier wins when several are granted.
	both := EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		enabled: map[string]bool{
			FeatureBackupConfig:     true,
			FeatureBackupStoragePro: true,
			FeatureBackupStorageAyn: true,
		},
	}, "user-1", false)
	if both.QuotaBytes != int64(500)<<30 || both.GrantedStorageFeature != FeatureBackupStorageAyn {
		t.Fatalf("largest granted tier must win: %+v", both)
	}
}

// TestAccountWithoutStorageKeyGetsTheSmallestBudget is the regression this
// package most needs. Resolving only the top key and falling through to a paid
// default handed every account below all-you-need - including starter, which
// publishes a far smaller figure - the pro budget.
func TestAccountWithoutStorageKeyGetsTheSmallestBudget(t *testing.T) {
	ctx := context.Background()

	decision := EvaluateBackupEntitlement(ctx, &fakeBackupChecker{
		enabled: map[string]bool{FeatureBackupConfig: true},
	}, "user-1", false)

	if decision.Denied {
		t.Fatalf("config-class backup must still be granted: %+v", decision)
	}
	if decision.QuotaBytes != int64(50)<<30 {
		t.Fatalf("no storage key resolved to %s, want the starter budget", formatBytes(decision.QuotaBytes))
	}
	if decision.GrantedStorageFeature != FeatureBackupStorageStarter {
		t.Fatalf("granted key = %q, want the starter key", decision.GrantedStorageFeature)
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
	if got := EvaluateBackupEntitlement(ctx, checker, "user-1", true).QuotaBytes; got != int64(500)<<30 {
		t.Fatalf("invalid override must fall back: %d", got)
	}

	proChecker := &fakeBackupChecker{enabled: map[string]bool{
		FeatureBackupConfig:     true,
		FeatureBackupStoragePro: true,
	}}
	t.Setenv("TECHSTACK_BACKUP_QUOTA_PRO_GB", "10")
	if got := EvaluateBackupEntitlement(ctx, proChecker, "user-1", false).QuotaBytes; got != int64(10)<<30 {
		t.Fatalf("pro override: %d", got)
	}

	starterChecker := &fakeBackupChecker{enabled: map[string]bool{FeatureBackupConfig: true}}
	t.Setenv("TECHSTACK_BACKUP_QUOTA_STARTER_GB", "5")
	if got := EvaluateBackupEntitlement(ctx, starterChecker, "user-1", false).QuotaBytes; got != int64(5)<<30 {
		t.Fatalf("starter override: %d", got)
	}
}

// TestQuotaDenialNamesTheCallersOwnTier covers the second half of the defect:
// the envelope reported the all-you-need key to every caller, telling a
// starter account it was missing an entitlement it had never been offered,
// and offered a snapshot-deletion step the product exposes no verb for.
func TestQuotaDenialNamesTheCallersOwnTier(t *testing.T) {
	starter := BackupQuotaExceededDetails(int64(50)<<30, int64(60)<<30, FeatureBackupStorageStarter)
	required, _ := starter["required_features"].([]string)
	missing, _ := starter["missing_features"].([]string)
	if len(required) != 1 || required[0] != FeatureBackupStorageStarter {
		t.Fatalf("required_features = %v, want the caller's own tier", required)
	}
	if len(missing) != 1 || missing[0] != FeatureBackupStoragePro {
		t.Fatalf("missing_features = %v, want the next tier up", missing)
	}

	// On the largest tier there is no upgrade to offer, so none is claimed.
	ayn := BackupQuotaExceededDetails(int64(500)<<30, int64(600)<<30, FeatureBackupStorageAyn)
	aynMissing, _ := ayn["missing_features"].([]string)
	if len(aynMissing) != 0 {
		t.Fatalf("missing_features on the top tier = %v, want none", aynMissing)
	}

	guidance, _ := ayn["user_guidance"].(map[string]any)
	steps, _ := guidance["next_steps"].([]string)
	for _, step := range steps {
		if strings.Contains(strings.ToLower(step), "delete old snapshots") {
			t.Fatalf("guidance offers a snapshot-deletion verb the product does not expose: %q", step)
		}
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

	quota := BackupQuotaExceededDetails(int64(250)<<30, int64(260)<<30, FeatureBackupStoragePro)
	if quota["error_code"] != BackupQuotaExceededErrorCode || quota["reason_code"] != ReasonQuotaExceeded {
		t.Fatalf("quota envelope: %v", quota)
	}
	guidance := quota["user_guidance"].(map[string]any)
	if body := guidance["body"].(string); !strings.Contains(body, "260.0 GB") || !strings.Contains(body, "250.0 GB") {
		t.Fatalf("quota guidance body: %s", body)
	}
}
