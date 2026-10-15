package stackkitrelease

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kombifyio/stackkits/pkg/models"
	"gopkg.in/yaml.v3"
)

//go:embed pinned-kit-metadata.json
var pinnedKitMetadataJSON []byte

var ErrKitMetadataNotFound = errors.New("StackKit metadata not found")

const maxKitMetadataBytes = 1 << 20

type SDKMetadataSource struct {
	Module       string `json:"module"`
	Version      string `json:"version"`
	ModuleSum    string `json:"moduleSum"`
	SourceCommit string `json:"sourceCommit"`
}

func (source SDKMetadataSource) MatchesCatalog(release UseCaseCatalogRelease) bool {
	return release.Tag == source.Version && release.PublicSourceSHA == source.SourceCommit
}

type pinnedMetadataDocument struct {
	SchemaVersion string            `json:"schemaVersion"`
	Source        SDKMetadataSource `json:"source"`
	Documents     []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		YAML   string `json:"yaml"`
	} `json:"documents"`
	ApplicationMetadata *struct {
		Path         string `json:"path"`
		SourceSHA256 string `json:"sourceSHA256"`
		SHA256       string `json:"sha256"`
		JSON         string `json:"json"`
	} `json:"applicationMetadata,omitempty"`
	AgentSurfaces *struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		JSON   string `json:"json"`
	} `json:"agentSurfaces,omitempty"`
}

func decodePinnedMetadata(data []byte) (pinnedMetadataDocument, error) {
	var document pinnedMetadataDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return document, fmt.Errorf("decode pinned kit metadata: %w", err)
	}
	if document.SchemaVersion != "techstack.stackkits-kit-metadata/v1" || document.Source.Module != "github.com/kombifyio/stackkits" ||
		document.Source.Version == "" || !strings.HasPrefix(document.Source.ModuleSum, "h1:") || len(document.Source.SourceCommit) != 40 || len(document.Documents) == 0 {
		return document, fmt.Errorf("pinned kit metadata has incomplete source identity")
	}
	return document, nil
}

// KitMetadata retains the owning StackKits metadata type. The additional
// display fields project declared YAML values only; they do not admit a kit,
// workload, mode or platform for execution.
type KitMetadata struct {
	Metadata    models.KitMetadata
	Deprecated  bool
	SupportedOS []string
	Features    []string
	Services    KitMetadataServices
	Modes       map[string]KitMetadataMode
}

type KitMetadataServices struct {
	Required    []string `json:"required,omitempty" yaml:"required"`
	Recommended []string `json:"recommended,omitempty" yaml:"recommended"`
	Available   []string `json:"available,omitempty" yaml:"available"`
}

type KitMetadataMode struct {
	models.ModeSpec `yaml:",inline"`
	TemplateDir     string   `yaml:"templateDir"`
	Requires        []string `yaml:"requires"`
	RecommendedFor  []string `yaml:"recommended_for"`
}

// ReadKitMetadata uses the pinned published projection when no override is
// configured. An explicit directory is always its own authority: missing or
// invalid metadata fails instead of switching to another source.
func ReadKitMetadata(directory string) ([]KitMetadata, error) {
	if directory == "" {
		return PinnedKitMetadata()
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read configured StackKits metadata directory: %w", err)
	}
	var result []KitMetadata
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		fileName := filepath.Join(directory, entry.Name(), "stackkit.yaml")
		file, info, err := openRegularFile(fileName, maxKitMetadataBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read configured kit metadata %s: %w", entry.Name(), err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxKitMetadataBytes+1))
		stableErr := ensureUnchanged(file, info)
		closeErr := file.Close()
		if err := errors.Join(readErr, stableErr, closeErr); err != nil {
			return nil, fmt.Errorf("read configured kit metadata %s: %w", entry.Name(), err)
		}
		metadata, err := decodeKitMetadata(data, entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, metadata)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("configured StackKits directory contains no kit metadata")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Metadata.Name < result[j].Metadata.Name })
	return result, nil
}

// PinnedKitMetadata decodes the checksum-bound owning YAML carried by the
// exact public module pin. It requires no runtime source checkout or CUE load.
func PinnedKitMetadata() ([]KitMetadata, error) {
	document, err := decodePinnedMetadata(pinnedKitMetadataJSON)
	if err != nil {
		return nil, err
	}
	result := make([]KitMetadata, 0, len(document.Documents))
	seen := map[string]bool{}
	for _, entry := range document.Documents {
		digest := sha256.Sum256([]byte(entry.YAML))
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return nil, fmt.Errorf("pinned kit metadata digest mismatch: %s", entry.Path)
		}
		name := path.Dir(entry.Path)
		if path.Base(entry.Path) != "stackkit.yaml" || path.Base(name) != name || seen[name] {
			return nil, fmt.Errorf("pinned kit metadata path is invalid or duplicated")
		}
		metadata, err := decodeKitMetadata([]byte(entry.YAML), name)
		if err != nil {
			return nil, err
		}
		result = append(result, metadata)
		seen[name] = true
	}
	return result, nil
}

func decodeKitMetadata(data []byte, name string) (KitMetadata, error) {
	var kit models.StackKit
	if err := yaml.Unmarshal(data, &kit); err != nil {
		return KitMetadata{}, fmt.Errorf("decode StackKits metadata %s: %w", name, err)
	}
	if (kit.APIVersion != "stackkit/v1" || kit.Kind != "StackKit") &&
		(kit.APIVersion != "stackkit.catalog/v1alpha1" || kit.Kind != "KitCatalogMetadata") {
		return KitMetadata{}, fmt.Errorf("unsupported StackKits metadata document %s", name)
	}
	if kit.Metadata.Name != name || strings.TrimSpace(kit.Metadata.Version) == "" || strings.TrimSpace(kit.Metadata.DisplayName) == "" {
		return KitMetadata{}, fmt.Errorf("StackKits metadata %s has incomplete or mismatched metadata identity", name)
	}
	// These published fields extend the owner's compact Go display types.
	// Decode their values without inventing mode, service or feature defaults.
	var display struct {
		Metadata struct {
			Deprecated bool `yaml:"deprecated"`
		} `yaml:"metadata"`
		Features map[string]bool            `yaml:"features"`
		Services KitMetadataServices        `yaml:"services"`
		Modes    map[string]KitMetadataMode `yaml:"modes"`
	}
	if err := yaml.Unmarshal(data, &display); err != nil {
		return KitMetadata{}, fmt.Errorf("decode StackKits display metadata %s: %w", name, err)
	}
	result := KitMetadata{Metadata: kit.Metadata, Deprecated: display.Metadata.Deprecated, SupportedOS: kit.SupportedOS, Services: display.Services, Modes: display.Modes}
	for feature, enabled := range display.Features {
		if enabled {
			result.Features = append(result.Features, feature)
		}
	}
	sort.Strings(result.Features)
	return result, nil
}
