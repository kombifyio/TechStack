package stackkits

import (
	"testing"
)

func TestLoadEmbeddedStackKit(t *testing.T) {
	tests := []struct {
		name       string
		kitName    string
		version    string
		wantErr    bool
		wantSource string
	}{
		{
			name:       "load basement-kit embedded",
			kitName:    "basement-kit",
			version:    "latest",
			wantErr:    false,
			wantSource: "embedded",
		},
		{
			name:       "load basement-kit fallback",
			kitName:    "basement-kit",
			version:    "",
			wantErr:    false,
			wantSource: "embedded",
		},
		{
			name:    "non-existent kit returns error",
			kitName: "non-existent-kit",
			version: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kit, err := loadEmbeddedStackKit(tt.kitName, tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("loadEmbeddedStackKit() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if kit == nil {
					t.Error("loadEmbeddedStackKit() returned nil kit")
					return
				}
				if kit.Meta.Name != tt.kitName {
					t.Errorf("kit.Meta.Name = %v, want %v", kit.Meta.Name, tt.kitName)
				}
				// Accept both embedded and embedded-fallback source types
				if kit.Source.Type != "embedded" && kit.Source.Type != "embedded-fallback" {
					t.Errorf("kit.Source.Type = %v, want embedded or embedded-fallback", kit.Source.Type)
				}
			}
		})
	}
}

func TestListEmbeddedStackKits(t *testing.T) {
	kits := ListEmbeddedStackKits()
	if len(kits) == 0 {
		t.Error("ListEmbeddedStackKits() returned empty list")
	}

	// basement-kit should always be in the list
	found := false
	for _, kit := range kits {
		if kit == "basement-kit" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ListEmbeddedStackKits() does not include basement-kit")
	}
}

func TestHasEmbeddedStackKit(t *testing.T) {
	tests := []struct {
		name    string
		kitName string
		want    bool
	}{
		{
			name:    "basement-kit exists",
			kitName: "basement-kit",
			want:    true,
		},
		{
			name:    "non-existent kit",
			kitName: "does-not-exist",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasEmbeddedStackKit(tt.kitName)
			if got != tt.want {
				t.Errorf("HasEmbeddedStackKit(%q) = %v, want %v", tt.kitName, got, tt.want)
			}
		})
	}
}

func TestCreateFallbackStackKit(t *testing.T) {
	kit := createFallbackStackKit("test-kit", "v1.0.0")

	if kit == nil {
		t.Fatal("createFallbackStackKit() returned nil")
	}

	if kit.Meta.Name != "test-kit" {
		t.Errorf("kit.Meta.Name = %v, want test-kit", kit.Meta.Name)
	}

	if kit.Source.Type != "embedded-fallback" {
		t.Errorf("kit.Source.Type = %v, want embedded-fallback", kit.Source.Type)
	}

	if kit.Source.Version != "v1.0.0" {
		t.Errorf("kit.Source.Version = %v, want v1.0.0", kit.Source.Version)
	}

	if kit.Templates == nil {
		t.Error("kit.Templates is nil, want initialized map")
	}

	if kit.Variants.OS == nil {
		t.Error("kit.Variants.OS is nil, want initialized map")
	}
}

func TestEmbeddedStackKitContent(t *testing.T) {
	// Test that embedded basement-kit has expected content
	kit, err := loadEmbeddedStackKit("basement-kit", "")
	if err != nil {
		t.Fatalf("loadEmbeddedStackKit(basement-kit) failed: %v", err)
	}

	// Check metadata
	if kit.Meta.Version == "" {
		t.Error("kit.Meta.Version is empty")
	}

	if kit.Meta.Description == "" {
		t.Error("kit.Meta.Description is empty")
	}

	// Mode should be set
	if !kit.Meta.Mode.Simple && !kit.Meta.Mode.Advanced {
		t.Error("kit.Meta.Mode has no modes enabled")
	}
}
