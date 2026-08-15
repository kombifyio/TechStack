// Package runtimeinventorygen deterministically projects the Techstack-owned
// runtime inventory schema into the dependency-light public Go wire.
package runtimeinventorygen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SchemaVersion    = "techstack.runtimeinventory-schema/v1"
	GeneratorVersion = "runtimeinventorygen/v1"
	WireVersion      = "runtimeinventory/v1"
	ManifestVersion  = "techstack.runtimeinventory-generation-manifest/v1"
)

var (
	goIdentifier = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	jsonName     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

type Schema struct {
	SchemaVersion    string       `json:"schemaVersion"`
	GeneratorVersion string       `json:"generatorVersion"`
	WireVersion      string       `json:"wireVersion"`
	PackageName      string       `json:"packageName"`
	SourceRepository string       `json:"sourceRepository"`
	SourcePath       string       `json:"sourcePath"`
	Imports          []string     `json:"imports"`
	Types            []TypeSchema `json:"types"`
}

type TypeSchema struct {
	Name   string        `json:"name"`
	Doc    []string      `json:"doc"`
	Fields []FieldSchema `json:"fields"`
}

type FieldSchema struct {
	Name      string `json:"name"`
	GoType    string `json:"goType"`
	JSON      string `json:"json"`
	OmitEmpty bool   `json:"omitempty,omitempty"`
}

type Manifest struct {
	SchemaVersion    string `json:"schemaVersion"`
	GeneratorVersion string `json:"generatorVersion"`
	WireVersion      string `json:"wireVersion"`
	Source           struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		SHA256     string `json:"sha256"`
	} `json:"source"`
	Outputs map[string]string `json:"outputs"`
}

func Generate(schemaPath, outputDir string) error {
	schema, source, err := Load(schemaPath)
	if err != nil {
		return err
	}
	generated, err := Render(schema)
	if err != nil {
		return err
	}
	manifest, err := RenderManifest(schema, source, generated)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "types.go"), generated, 0o644); err != nil {
		return fmt.Errorf("write types.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "manifest.json"), manifest, 0o644); err != nil {
		return fmt.Errorf("write manifest.json: %w", err)
	}
	return nil
}

func Load(path string) (Schema, []byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Schema{}, nil, fmt.Errorf("read schema: %w", err)
	}
	var schema Schema
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&schema); err != nil {
		return Schema{}, nil, fmt.Errorf("decode schema: %w", err)
	}
	if err := Validate(schema); err != nil {
		return Schema{}, nil, err
	}
	return schema, payload, nil
}

func Validate(schema Schema) error {
	if schema.SchemaVersion != SchemaVersion || schema.GeneratorVersion != GeneratorVersion || schema.WireVersion != WireVersion {
		return fmt.Errorf("unsupported schema/generator/wire identity")
	}
	if schema.PackageName != "runtimeinventory" {
		return fmt.Errorf("packageName must be runtimeinventory")
	}
	if schema.SourceRepository != "github.com/kombifyio/TechStack" || schema.SourcePath != "contracts/runtimeinventory/v1/schema.json" {
		return fmt.Errorf("schema source authority is not canonical Techstack")
	}
	if len(schema.Imports) != 1 || schema.Imports[0] != "time" {
		return fmt.Errorf("imports must contain only time")
	}
	if len(schema.Types) == 0 {
		return fmt.Errorf("types must not be empty")
	}
	typeNames := make(map[string]struct{}, len(schema.Types))
	for _, typ := range schema.Types {
		if !goIdentifier.MatchString(typ.Name) {
			return fmt.Errorf("invalid type name %q", typ.Name)
		}
		if _, exists := typeNames[typ.Name]; exists {
			return fmt.Errorf("duplicate type %q", typ.Name)
		}
		typeNames[typ.Name] = struct{}{}
		if len(typ.Doc) == 0 || len(typ.Fields) == 0 {
			return fmt.Errorf("type %s must declare documentation and fields", typ.Name)
		}
	}
	for _, typ := range schema.Types {
		fieldNames := map[string]struct{}{}
		jsonNames := map[string]struct{}{}
		for _, field := range typ.Fields {
			if !goIdentifier.MatchString(field.Name) || !jsonName.MatchString(field.JSON) {
				return fmt.Errorf("type %s contains invalid field identity %q/%q", typ.Name, field.Name, field.JSON)
			}
			if _, exists := fieldNames[field.Name]; exists {
				return fmt.Errorf("type %s contains duplicate field %s", typ.Name, field.Name)
			}
			if _, exists := jsonNames[field.JSON]; exists {
				return fmt.Errorf("type %s contains duplicate JSON field %s", typ.Name, field.JSON)
			}
			fieldNames[field.Name] = struct{}{}
			jsonNames[field.JSON] = struct{}{}
			lower := strings.ToLower(field.JSON)
			for _, forbidden := range []string{"credential", "token", "secret", "metadata", "endpoint_ref", "route_id", "auth_hint", "ssh", "command", "logs"} {
				if strings.Contains(lower, forbidden) {
					return fmt.Errorf("type %s contains forbidden secret/open field %s", typ.Name, field.JSON)
				}
			}
			if !validGoType(field.GoType, typeNames) {
				return fmt.Errorf("type %s field %s uses unsupported Go type %q", typ.Name, field.Name, field.GoType)
			}
		}
	}
	return nil
}

func validGoType(value string, schemaTypes map[string]struct{}) bool {
	base := value
	if strings.HasPrefix(base, "*") {
		base = strings.TrimPrefix(base, "*")
	} else if strings.HasPrefix(base, "[]") {
		base = strings.TrimPrefix(base, "[]")
	}
	if strings.HasPrefix(base, "*") || strings.HasPrefix(base, "[]") || strings.ContainsAny(base, "[]{}() ") {
		return false
	}
	switch base {
	case "bool", "int", "int64", "string", "time.Time":
		return true
	default:
		_, ok := schemaTypes[base]
		return ok
	}
}

func Render(schema Schema) ([]byte, error) {
	var output bytes.Buffer
	fmt.Fprintf(&output, "package %s\n\n", schema.PackageName)
	if len(schema.Imports) == 1 {
		fmt.Fprintf(&output, "import %q\n\n", schema.Imports[0])
	} else if len(schema.Imports) > 1 {
		output.WriteString("import (\n")
		for _, imported := range schema.Imports {
			fmt.Fprintf(&output, "\t%q\n", imported)
		}
		output.WriteString(")\n\n")
	}
	for _, typ := range schema.Types {
		for _, line := range typ.Doc {
			fmt.Fprintf(&output, "// %s\n", line)
		}
		fmt.Fprintf(&output, "type %s struct {\n", typ.Name)
		for _, field := range typ.Fields {
			tag := field.JSON
			if field.OmitEmpty {
				tag += ",omitempty"
			}
			fmt.Fprintf(&output, "\t%s %s `json:\"%s\"`\n", field.Name, field.GoType, tag)
		}
		output.WriteString("}\n\n")
	}
	formatted, err := format.Source(output.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated Go: %w", err)
	}
	return formatted, nil
}

func RenderManifest(schema Schema, source, generated []byte) ([]byte, error) {
	manifest := Manifest{
		SchemaVersion:    ManifestVersion,
		GeneratorVersion: schema.GeneratorVersion,
		WireVersion:      schema.WireVersion,
		Outputs:          map[string]string{"types.go": digest(generated)},
	}
	manifest.Source.Repository = schema.SourceRepository
	manifest.Source.Path = schema.SourcePath
	manifest.Source.SHA256 = digest(source)
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return append(payload, '\n'), nil
}

func Check(schemaPath, outputDir string) error {
	schema, source, err := Load(schemaPath)
	if err != nil {
		return err
	}
	generated, err := Render(schema)
	if err != nil {
		return err
	}
	manifest, err := RenderManifest(schema, source, generated)
	if err != nil {
		return err
	}
	for name, expected := range map[string][]byte{"types.go": generated, "manifest.json": manifest} {
		actual, readErr := os.ReadFile(filepath.Join(outputDir, name))
		if readErr != nil {
			return fmt.Errorf("read checked-in %s: %w", name, readErr)
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("checked-in %s differs from deterministic generation", name)
		}
	}
	return nil
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
