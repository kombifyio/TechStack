package unifier

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealStackKitCompiles loads a kit from an actual StackKits tree, which is
// the only thing that proves the combined-buffer construction survives the CUE
// the published kits really contain. Unit fixtures did not: they never imported
// "struct", so the missing allowlist entry stayed invisible until a live
// managed-runtime rollout failed on it.
//
// Point TECHSTACK_STACKKITS_TESTDIR at a StackKits checkout to run it, e.g.
//
//	TECHSTACK_STACKKITS_TESTDIR=../../../kombify-StackKits go test ./pkg/unifier/...
func TestRealStackKitCompiles(t *testing.T) {
	root := os.Getenv("TECHSTACK_STACKKITS_TESTDIR")
	if root == "" {
		t.Skip("set TECHSTACK_STACKKITS_TESTDIR to a StackKits checkout")
	}
	if _, err := os.Stat(filepath.Join(root, "base")); err != nil {
		t.Skipf("no base/ under %s: %v", root, err)
	}

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	for _, kit := range []string{"cloud-kit", "basement-kit"} {
		t.Run(kit, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(root, kit)); err != nil {
				t.Skipf("kit %s absent: %v", kit, err)
			}
			value, err := loader.LoadKit(kit)
			if err != nil {
				t.Fatalf("LoadKit(%s) failed: %v", kit, err)
			}
			if value.Err() != nil {
				t.Fatalf("kit %s did not compile: %v", kit, value.Err())
			}
		})
	}
}
