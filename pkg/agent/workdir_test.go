package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAgentWorkDir(t *testing.T) {
	base := t.TempDir()
	absInside := filepath.Join(base, "explicit", "inside")

	tests := []struct {
		name      string
		commandID string
		requested string
		wantErr   bool
	}{
		{name: "default command id", commandID: "cmd-1", requested: ""},
		{name: "relative child", commandID: "cmd-2", requested: filepath.Join("nested", "work")},
		{name: "absolute inside base", commandID: "cmd-3", requested: absInside},
		{name: "missing command id for default", commandID: "", requested: "", wantErr: true},
		{name: "relative traversal", commandID: "cmd-4", requested: filepath.Join("..", "outside"), wantErr: true},
		{name: "absolute outside base", commandID: "cmd-5", requested: filepath.Join(filepath.Dir(base), "outside"), wantErr: true},
		{name: "windows absolute path", commandID: "cmd-6", requested: `C:\Windows`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgentWorkDir(base, tt.commandID, tt.requested)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("resolved path is not absolute: %q", got)
			}
			rel, err := filepath.Rel(base, got)
			if err != nil {
				t.Fatalf("rel: %v", err)
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("resolved path escaped base: %q", got)
			}
		})
	}
}
