package providercontrol

import (
	"errors"
	"testing"
)

func TestResourceFreeTeardownAcceptsCurrentDecommissionGeneration(t *testing.T) {
	generation, err := resourceFreeTeardownServerGeneration(1, 2, true)
	if err != nil {
		t.Fatalf("generation fence: %v", err)
	}
	if generation != 2 {
		t.Fatalf("generation = %d, want current decommission generation 2", generation)
	}
}

func TestResourceFreeTeardownRejectsGenerationRegression(t *testing.T) {
	if _, err := resourceFreeTeardownServerGeneration(2, 1, true); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("error = %v, want ErrLeaseFence", err)
	}
}

func TestResourceFreeTeardownRequiresTeardownIntentForAdvancedGeneration(t *testing.T) {
	if _, err := resourceFreeTeardownServerGeneration(1, 2, false); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("error = %v, want ErrLeaseFence", err)
	}
}
