package unifier

import "testing"

func TestCanonicalStackKitName(t *testing.T) {
	tests := map[string]string{
		"basement-kit": StackKitBasement,
		"basement":     StackKitBasement,
		"base-kit":     "base-kit",
		"cloud-kit":    StackKitCloud,
		"cloud":        StackKitCloud,
	}

	for input, want := range tests {
		if got := CanonicalStackKitName(input); got != want {
			t.Fatalf("CanonicalStackKitName(%q) = %q, want %q", input, got, want)
		}
	}
}
