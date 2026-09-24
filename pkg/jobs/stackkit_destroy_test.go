package jobs

import (
	"context"
	"testing"
)

func TestDestroyStackKitWorkspaceRequiresRefs(t *testing.T) {
	if err := destroyStackKitWorkspace(context.Background(), "", "stack"); err == nil {
		t.Fatal("empty workDir succeeded")
	}
	if err := destroyStackKitWorkspace(context.Background(), t.TempDir(), ""); err == nil {
		t.Fatal("empty workload succeeded")
	}
}
