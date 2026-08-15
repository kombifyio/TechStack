package providererrors

import "testing"

func TestClassifyMessageExtractsIONOSThrottle(t *testing.T) {
	err := `simulate enroll returned 502: {"error":{"code":502,"message":"create ionos-managed node: ionos create server request failed: [VDC-5-1091] Storage creation on SSD failed due to too many recent create and delete operations. Please contact support."}}`

	info := ClassifyMessage(err)

	if info.Provider != "ionos-managed" {
		t.Fatalf("provider = %q, want ionos-managed", info.Provider)
	}
	if info.Code != "VDC-5-1091" {
		t.Fatalf("code = %q, want VDC-5-1091", info.Code)
	}
	if info.Category != CategoryProviderThrottle || !info.Terminal {
		t.Fatalf("classification = %+v, want terminal provider throttle", info)
	}
	if info.RetryHint != RetryHintRetryLater {
		t.Fatalf("retry hint = %q, want %q", info.RetryHint, RetryHintRetryLater)
	}
	if info.Summary != "[VDC-5-1091] Storage creation on SSD failed due to too many recent create and delete operations. Please contact support." {
		t.Fatalf("summary = %q", info.Summary)
	}
}

func TestClassifyMessageExtractsProviderQuota(t *testing.T) {
	info := ClassifyMessage(`create ionos-managed node: [VDC-5-1051] personal limit would be exhausted`)

	if info.Code != "VDC-5-1051" {
		t.Fatalf("code = %q, want VDC-5-1051", info.Code)
	}
	if info.Category != CategoryProviderQuota || !info.Terminal {
		t.Fatalf("classification = %+v, want terminal provider quota", info)
	}
	if info.RetryHint != RetryHintFreeResources {
		t.Fatalf("retry hint = %q, want %q", info.RetryHint, RetryHintFreeResources)
	}
}

func TestClassifyMessageKeepsUnknownProviderErrorRetryable(t *testing.T) {
	info := ClassifyMessage(`simulate enroll returned 502: create centron-managed node: upstream unavailable`)

	if info.Provider != "centron-managed" {
		t.Fatalf("provider = %q, want centron-managed", info.Provider)
	}
	if info.Category != CategoryProviderError {
		t.Fatalf("category = %q, want provider_error", info.Category)
	}
	if info.Terminal {
		t.Fatalf("terminal = true, want retryable unknown provider error")
	}
}
