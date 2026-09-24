package providercontroljobs

import (
	"strings"
	"testing"
)

func retryCandidate(failedAttempts int) nativeDecommissionCandidate {
	return nativeDecommissionCandidate{
		LeaseID:            "lease-8a10de654a3af5d3d3d661ac846bd29e",
		ResourceGeneration: "9c002e8c-82a6-4349-b025-2d4ff520f566",
		FailedAttempts:     failedAttempts,
		// Every failure in these fixtures is recent, so all of them are charged
		// against the budget.
		RecentFailedAttempts: failedAttempts,
	}
}

const retryBaseKey = "native-decommission:lease-8a10de654a3af5d3d3d661ac846bd29e:9c002e8c-82a6-4349-b025-2d4ff520f566"

// The first attempt must keep the historical key. A decommission that is
// already in flight has to replay onto its own operation rather than fork a
// second one against the same provider resources.
func TestFirstAttemptKeepsTheHistoricalKey(t *testing.T) {
	if got := nativeDecommissionIdempotencyKey(retryCandidate(0)); got != retryBaseKey {
		t.Fatalf("key = %q, want the unchanged base key %q", got, retryBaseKey)
	}
}

// The defect this pins: the key carried no attempt, so an at-most-once lookup
// returned the same terminally failed record on every later destroy. The
// provider resource kept billing, the RuntimeServer never reached its tombstone,
// and the capacity slot was reserved for good -- live on 2026-07-27 the demo
// account held 12 of 12 managed-server slots against one running server and was
// denied with managed_runtime_max_servers_reached.
func TestARetryAfterAFailedAttemptStartsANewOperation(t *testing.T) {
	first := nativeDecommissionIdempotencyKey(retryCandidate(0))
	second := nativeDecommissionIdempotencyKey(retryCandidate(1))

	if second == first {
		t.Fatal("the retry reuses the failed attempt's key; it will replay the same terminal verdict forever")
	}
	if !strings.HasPrefix(second, retryBaseKey+":") {
		t.Fatalf("retry key %q must extend the base key so past attempts stay countable", second)
	}
}

// Each attempt needs its own identity, otherwise attempt three would replay
// attempt two's verdict.
func TestEveryAttemptHasADistinctKey(t *testing.T) {
	seen := make(map[string]int, maxNativeDecommissionAttempts)
	for attempt := 0; attempt < maxNativeDecommissionAttempts; attempt++ {
		key := nativeDecommissionIdempotencyKey(retryCandidate(attempt))
		if previous, exists := seen[key]; exists {
			t.Fatalf("attempts %d and %d share key %q", previous, attempt, key)
		}
		seen[key] = attempt
	}
}

// The key is a durable operation identity, so the same candidate must always
// derive the same key. A non-deterministic key would open a new provider
// operation on every pass of the job.
func TestKeyDerivationIsDeterministic(t *testing.T) {
	candidate := retryCandidate(2)
	if first, second := nativeDecommissionIdempotencyKey(candidate), nativeDecommissionIdempotencyKey(candidate); first != second {
		t.Fatalf("key is not stable: %q then %q", first, second)
	}
}

// Two generations of the same lease must never share an attempt series: a
// replaced VM has its own provider resources and its own capacity reservation.
func TestDifferentGenerationsDoNotShareAttempts(t *testing.T) {
	other := retryCandidate(1)
	other.ResourceGeneration = "11111111-2222-3333-4444-555555555555"

	if nativeDecommissionIdempotencyKey(other) == nativeDecommissionIdempotencyKey(retryCandidate(1)) {
		t.Fatal("two generations derived the same decommission key")
	}
}

// The attempt budget has to leave room for at least one retry, or the fix is
// cosmetic.
func TestAttemptBudgetAllowsAtLeastOneRetry(t *testing.T) {
	if maxNativeDecommissionAttempts < 2 {
		t.Fatalf("maxNativeDecommissionAttempts = %d; a failed decommission could never be retried", maxNativeDecommissionAttempts)
	}
}
