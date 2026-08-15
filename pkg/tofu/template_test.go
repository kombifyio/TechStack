package tofu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewTemplateRenderer(t *testing.T) {
	templateDir := "/templates"
	outputDir := "/output"

	renderer := NewTemplateRenderer(templateDir, outputDir)

	if renderer == nil {
		t.Fatal("NewTemplateRenderer returned nil")
	}
	if renderer.TemplateDir != templateDir {
		t.Errorf("TemplateDir = %v, want %v", renderer.TemplateDir, templateDir)
	}
	if renderer.OutputDir != outputDir {
		t.Errorf("OutputDir = %v, want %v", renderer.OutputDir, outputDir)
	}
}

func TestTemplateRenderer_Render(t *testing.T) {
	t.Run("renders template with variables", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		// Create a template file
		templateContent := `resource "null_resource" "test" {
  triggers = {
    name = "{{ .Name }}"
    count = "{{ .Count }}"
  }
}
`
		templateFile := "test.tf"
		if err := os.WriteFile(filepath.Join(templateDir, templateFile), []byte(templateContent), 0644); err != nil {
			t.Fatalf("failed to create template: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		vars := map[string]interface{}{
			"Name":  "test-resource",
			"Count": 5,
		}

		err := renderer.Render(templateFile, vars)
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}

		// Verify output file exists
		outputPath := filepath.Join(outputDir, templateFile)
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("output file was not created")
		}

		// Verify content
		content, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("failed to read output: %v", err)
		}
		if len(content) == 0 {
			t.Error("output file is empty")
		}
	})

	t.Run("returns error for nonexistent template", func(t *testing.T) {
		renderer := NewTemplateRenderer("/nonexistent", t.TempDir())
		err := renderer.Render("missing.tf", nil)
		if err == nil {
			t.Error("expected error for nonexistent template")
		}
	})

	t.Run("returns error for invalid template syntax", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		// Create an invalid template
		invalidTemplate := `{{ .InvalidSyntax`
		if err := os.WriteFile(filepath.Join(templateDir, "invalid.tf"), []byte(invalidTemplate), 0644); err != nil {
			t.Fatalf("failed to create invalid template: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		err := renderer.Render("invalid.tf", nil)
		if err == nil {
			t.Error("expected error for invalid template syntax")
		}
	})

	t.Run("rejects template path traversal", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()
		renderer := NewTemplateRenderer(templateDir, outputDir)

		if err := renderer.Render("../escape.tf", nil); err == nil {
			t.Fatal("expected error for template path traversal")
		}
	})
}

func TestTemplateRenderer_RenderAll(t *testing.T) {
	t.Run("renders all .tpl files", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		// Create multiple template files
		template1 := `name: {{ .Name }}`
		template2 := `count: {{ .Count }}`
		if err := os.WriteFile(filepath.Join(templateDir, "file1.tpl"), []byte(template1), 0644); err != nil {
			t.Fatalf("failed to create template1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(templateDir, "file2.tpl"), []byte(template2), 0644); err != nil {
			t.Fatalf("failed to create template2: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		vars := map[string]interface{}{
			"Name":  "test",
			"Count": 10,
		}

		err := renderer.RenderAll(vars)
		if err != nil {
			t.Fatalf("RenderAll() error = %v", err)
		}

		// Verify both files were created
		if _, err := os.Stat(filepath.Join(outputDir, "file1.tpl")); os.IsNotExist(err) {
			t.Error("file1.tpl was not created")
		}
		if _, err := os.Stat(filepath.Join(outputDir, "file2.tpl")); os.IsNotExist(err) {
			t.Error("file2.tpl was not created")
		}
	})

	t.Run("returns empty for no templates", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		renderer := NewTemplateRenderer(templateDir, outputDir)
		err := renderer.RenderAll(nil)
		if err != nil {
			t.Errorf("RenderAll() error = %v, expected nil for empty dir", err)
		}
	})
}

func TestTemplateRenderer_CopyNonTemplates(t *testing.T) {
	t.Run("copies non-template files", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		// Create template and non-template files
		if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte("# main config"), 0644); err != nil {
			t.Fatalf("failed to create main.tf: %v", err)
		}
		if err := os.WriteFile(filepath.Join(templateDir, "template.tpl"), []byte("{{ .Value }}"), 0644); err != nil {
			t.Fatalf("failed to create template: %v", err)
		}
		if err := os.WriteFile(filepath.Join(templateDir, "variables.tf"), []byte("# variables"), 0644); err != nil {
			t.Fatalf("failed to create variables.tf: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		err := renderer.CopyNonTemplates()
		if err != nil {
			t.Fatalf("CopyNonTemplates() error = %v", err)
		}

		// Verify non-template files were copied
		if _, err := os.Stat(filepath.Join(outputDir, "main.tf")); os.IsNotExist(err) {
			t.Error("main.tf was not copied")
		}
		if _, err := os.Stat(filepath.Join(outputDir, "variables.tf")); os.IsNotExist(err) {
			t.Error("variables.tf was not copied")
		}

		// Verify template files were NOT copied
		if _, err := os.Stat(filepath.Join(outputDir, "template.tpl")); !os.IsNotExist(err) {
			t.Error("template.tpl should not have been copied")
		}
	})

	t.Run("handles subdirectories", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		// Create subdirectory with files
		subDir := filepath.Join(templateDir, "modules")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "module.tf"), []byte("# module"), 0644); err != nil {
			t.Fatalf("failed to create module.tf: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		err := renderer.CopyNonTemplates()
		if err != nil {
			t.Fatalf("CopyNonTemplates() error = %v", err)
		}

		// Verify subdirectory file was copied
		if _, err := os.Stat(filepath.Join(outputDir, "modules", "module.tf")); os.IsNotExist(err) {
			t.Error("modules/module.tf was not copied")
		}
	})

	t.Run("rejects symlinks", func(t *testing.T) {
		templateDir := t.TempDir()
		outputDir := t.TempDir()

		target := filepath.Join(templateDir, "target.tf")
		if err := os.WriteFile(target, []byte("# target"), 0600); err != nil {
			t.Fatalf("failed to create target: %v", err)
		}
		link := filepath.Join(templateDir, "link.tf")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable in this environment: %v", err)
		}

		renderer := NewTemplateRenderer(templateDir, outputDir)
		if err := renderer.CopyNonTemplates(); err == nil {
			t.Fatal("expected error for symlink in template tree")
		}
	})
}
