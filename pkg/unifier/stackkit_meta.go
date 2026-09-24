package unifier

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// stackKitYAMLMeta is the subset of stackkit.yaml needed for Unifier catalog
// display. Published StackKits YAML is the catalog source, not a Techstack registry.
type stackKitYAMLMeta struct {
	Name        string
	Version     string
	Description string
	Author      string
	License     string
	Tags        []string
	Features    []string
	SupportedOS []string
	Mode        stackKitYAMLMode
}

type stackKitYAMLMode struct {
	Simple   bool
	Advanced bool
}

type stackkitV1YAML struct {
	APIVersion string `yaml:"apiVersion"`
	Metadata   struct {
		Name        string   `yaml:"name"`
		Version     string   `yaml:"version"`
		Description string   `yaml:"description"`
		Author      string   `yaml:"author"`
		License     string   `yaml:"license"`
		Tags        []string `yaml:"tags"`
	} `yaml:"metadata"`
	SupportedOS []string            `yaml:"supportedOS"`
	Modes       map[string]struct{} `yaml:"modes"`
	Features    map[string]struct{} `yaml:"features"`
}

func parseStackKitMetaYAML(data []byte) (*stackKitYAMLMeta, error) {
	var probe struct {
		APIVersion string `yaml:"apiVersion"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if probe.APIVersion == "stackkit/v1" {
		var v1 stackkitV1YAML
		if err := yaml.Unmarshal(data, &v1); err != nil {
			return nil, fmt.Errorf("invalid stackkit/v1 format: %w", err)
		}
		_, hasSimple := v1.Modes["simple"]
		_, hasAdvanced := v1.Modes["advanced"]
		features := make([]string, 0, len(v1.Features))
		for name := range v1.Features {
			features = append(features, name)
		}
		return &stackKitYAMLMeta{
			Name:        v1.Metadata.Name,
			Version:     v1.Metadata.Version,
			Description: v1.Metadata.Description,
			Author:      v1.Metadata.Author,
			License:     v1.Metadata.License,
			Tags:        v1.Metadata.Tags,
			Features:    features,
			SupportedOS: v1.SupportedOS,
			Mode: stackKitYAMLMode{
				Simple:   hasSimple,
				Advanced: hasAdvanced,
			},
		}, nil
	}

	var flat struct {
		Name        string           `yaml:"name"`
		Version     string           `yaml:"version"`
		Description string           `yaml:"description"`
		Author      string           `yaml:"author"`
		License     string           `yaml:"license"`
		Tags        []string         `yaml:"tags"`
		Features    []string         `yaml:"features"`
		Mode        stackKitYAMLMode `yaml:"mode"`
		Requires    struct {
			OSSupported []string `yaml:"osSupported"`
		} `yaml:"requires"`
	}
	if err := yaml.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("invalid stackkit metadata: %w", err)
	}
	return &stackKitYAMLMeta{
		Name:        flat.Name,
		Version:     flat.Version,
		Description: flat.Description,
		Author:      flat.Author,
		License:     flat.License,
		Tags:        flat.Tags,
		Features:    flat.Features,
		SupportedOS: flat.Requires.OSSupported,
		Mode:        flat.Mode,
	}, nil
}
