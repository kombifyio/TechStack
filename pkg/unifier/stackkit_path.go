package unifier

import "strings"

const (
	StackKitBasement      = "basement-kit"
	StackKitCloud         = "cloud-kit"
	StackKitModernHomelab = "modern-homelab"
	StackKitHA            = "ha-kit"
	StackKitDevHomelab    = "dev-homelab"
)

// DefaultKnownStackKits retains the existing selection interface, backed by
// released metadata. An unreadable explicit authority never falls back.
func DefaultKnownStackKits() []string {
	names, _ := releasedStackKitNames()
	return names
}

func releasedStackKitNames() ([]string, error) {
	loader, err := NewStackKitLoader()
	if err != nil {
		return nil, err
	}
	kits, err := loader.ListKitInfo()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(kits))
	for _, kit := range kits {
		names = append(names, kit.Name)
	}
	return names, nil
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
