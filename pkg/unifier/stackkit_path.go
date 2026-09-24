package unifier

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	StackKitBasement      = "basement-kit"
	StackKitCloud         = "cloud-kit"
	StackKitModernHomelab = "modern-homelab"
	StackKitHA            = "ha-kit"
	StackKitDevHomelab    = "dev-homelab"
)

// DefaultStackKitsDir resolves an explicitly configured StackKits checkout.
// Production sets TECHSTACK_STACKKITS_DIR to the pinned published tree.
// A sibling workspace checkout is not implicit authority.
func DefaultStackKitsDir() string {
	for _, key := range []string{"TECHSTACK_STACKKITS_DIR", "STACKKITS_REPO", "STACKKITS_PATH"} {
		if dir := existingDir(os.Getenv(key)); dir != "" {
			return dir
		}
	}
	return ""
}

func DefaultKnownStackKits() []string {
	return []string{StackKitBasement, StackKitCloud, StackKitModernHomelab}
}

func IsSupportedProductStackKit(name string) bool {
	switch CanonicalStackKitName(name) {
	case StackKitBasement, StackKitCloud, StackKitModernHomelab:
		return true
	default:
		return false
	}
}

func CanonicalStackKitName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", StackKitBasement, "basement", "basementkit", "homelab-starter", "homelab-basic", "base-homelab", "minimal-arm":
		return StackKitBasement
	case StackKitCloud, "cloud", "cloudkit", "kombify-cloud-kit":
		return StackKitCloud
	case StackKitModernHomelab, "modern-kit", "homelab-advanced":
		return StackKitModernHomelab
	case StackKitHA, "ha-homelab", "hybrid-cloud", "cloud-native", "high-availability-homelab":
		return StackKitHA
	case StackKitDevHomelab, "dev-kit", "developer-local":
		return StackKitDevHomelab
	default:
		return strings.TrimSpace(name)
	}
}

func DiscoverAvailableStackKits(root string) []string {
	root = existingDir(root)
	if root == "" {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	kits := make([]string, 0)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(root, entry.Name(), "stackkit.yaml")
		if info, err := os.Lstat(metaPath); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		kit := CanonicalStackKitName(entry.Name())
		if kit == "" || !IsSupportedProductStackKit(kit) {
			continue
		}
		if _, ok := seen[kit]; ok {
			continue
		}
		seen[kit] = struct{}{}
		kits = append(kits, kit)
	}

	sort.Strings(kits)
	return kits
}

func existingDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(resolved); err == nil && st.IsDir() { // #nosec G703 -- path is operator-supplied StackKits checkout config; existence check only.
		return filepath.Clean(resolved)
	}
	return ""
}
