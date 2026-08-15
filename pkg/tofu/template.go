// Package tofu provides template rendering for OpenTofu configurations.
package tofu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// TemplateRenderer renders OpenTofu templates with variable substitution.
type TemplateRenderer struct {
	TemplateDir string
	OutputDir   string
}

// NewTemplateRenderer creates a new template renderer.
func NewTemplateRenderer(templateDir, outputDir string) *TemplateRenderer {
	return &TemplateRenderer{
		TemplateDir: templateDir,
		OutputDir:   outputDir,
	}
}

// Render renders a template file with the given variables.
func (tr *TemplateRenderer) Render(templateFile string, vars map[string]interface{}) error {
	templatePath, err := safeJoinWithinRoot(tr.TemplateDir, templateFile)
	if err != nil {
		return err
	}
	outputPath, err := safeJoinWithinRoot(tr.OutputDir, templateFile)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(templatePath)
	if statErr != nil {
		return fmt.Errorf("failed to inspect template %s: %w", templatePath, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("template %s must not be a symlink", templateFile)
	}

	// Read template file
	tmplContent, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read template %s: %w", templatePath, err)
	}

	// Parse template
	tmpl, err := template.New(templateFile).Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", templateFile, err)
	}

	// Create output directory if needed
	if mkdirErr := os.MkdirAll(filepath.Dir(outputPath), 0750); mkdirErr != nil {
		return fmt.Errorf("failed to create output directory: %w", mkdirErr)
	}

	// Create output file
	outputFile, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()

	// Execute template
	if err := tmpl.Execute(outputFile, vars); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// RenderAll renders all template files in the template directory.
func (tr *TemplateRenderer) RenderAll(vars map[string]interface{}) error {
	// Find all .tpl files
	pattern := filepath.Join(tr.TemplateDir, "*.tpl")
	templates, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find templates: %w", err)
	}

	for _, tmplPath := range templates {
		tmplFile := filepath.Base(tmplPath)
		if err := tr.Render(tmplFile, vars); err != nil {
			return err
		}
	}

	return nil
}

// CopyNonTemplates copies non-template files from template dir to output dir.
func (tr *TemplateRenderer) CopyNonTemplates() error {
	type copyTask struct {
		sourcePath string
		relPath    string
	}
	var tasks []copyTask

	if err := filepath.WalkDir(tr.TemplateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("template tree must not contain symlink %s", path)
		}

		// Skip template files
		if filepath.Ext(path) == ".tpl" {
			return nil
		}

		// Skip directories
		if entry.IsDir() {
			return nil
		}

		// Calculate relative path
		relPath, err := filepath.Rel(tr.TemplateDir, path)
		if err != nil {
			return err
		}

		tasks = append(tasks, copyTask{sourcePath: path, relPath: relPath})

		return nil
	}); err != nil {
		return err
	}

	for _, task := range tasks {
		if err := tr.copyNonTemplateFile(task.sourcePath, task.relPath); err != nil {
			return err
		}
	}

	return nil
}

func (tr *TemplateRenderer) copyNonTemplateFile(sourcePath string, relPath string) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", sourcePath, err)
	}

	outputPath, err := safeJoinWithinRoot(tr.OutputDir, relPath)
	if err != nil {
		return err
	}
	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", outputDir, err)
	}

	// #nosec G703 -- outputPath is root-confined by safeJoinWithinRoot using the validated relative template path.
	if err := os.WriteFile(outputPath, content, 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputPath, err)
	}

	return nil
}

func safeJoinWithinRoot(root, name string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("root directory is required")
	}
	if name == "" {
		return "", fmt.Errorf("path name is required")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("path %q must be relative", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root", name)
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes root", name)
	}
	return full, nil
}
