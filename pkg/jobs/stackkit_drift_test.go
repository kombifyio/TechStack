package jobs

import (
	"context"
	"testing"
)

func TestStackKitDriftOperationsRequireWorkDir(t *testing.T) {
	if _, err := detectStackKitDrift(context.Background(), "", 0); err == nil {
		t.Fatal("drift detection accepted an empty workDir")
	}
	if err := reconcileStackKitDrift(context.Background(), "", 0); err == nil {
		t.Fatal("drift reconciliation accepted an empty workDir")
	}
}
