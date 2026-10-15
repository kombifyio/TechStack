package stackkitrelease

// The stackkit.advanced-operations/v1 catalog is the StackKits contract for
// every Advanced operation an orchestrator may dispatch: the exact argv words
// after the program name with {placeholder} words, requirements, result
// payloads and the release status of each entry. StackKits owns it
// (internal/advancedcatalog, docs/data/advanced-operations/latest.json) and
// ships it inside every release archive. Techstack renders Advanced argv from
// the catalog of the exact pinned release and never hand-writes those flags.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	AdvancedOperationsSchemaVersion = "stackkit.advanced-operations/v1"
	// AdvancedOperationsArchivePath is the catalog's path inside a StackKits
	// release archive (.goreleaser.yaml archive files).
	AdvancedOperationsArchivePath = "docs/data/advanced-operations/latest.json"
	// AdvancedOperationsFileName is the catalog the image build extracts from
	// the digest-verified archive and places beside the release pin; the Agent
	// runtime bundle carries it at the same place.
	AdvancedOperationsFileName = "stackkits-advanced-operations-v1.json"

	maxAdvancedOperationsBytes = 1 << 20
	advancedOperationStatusOK  = "available"
)

// ErrAdvancedOperationsMissing reports a pinned release that does not carry
// its Advanced operations catalog. Every Techstack-managed deployment runs
// Advanced Mode, so such a release cannot serve a rollout: there is no
// embedded or Standard fallback.
var ErrAdvancedOperationsMissing = errors.New("the pinned StackKits release does not carry its Advanced operations catalog")

var advancedPlaceholderPattern = regexp.MustCompile(`^\{([A-Za-z][A-Za-z0-9]*)\}$`)

// AdvancedOperationsCatalog is the decoded catalog of one pinned release.
type AdvancedOperationsCatalog struct {
	SchemaVersion string
	// ReleaseVersion is the pinned release the catalog is evaluated against.
	ReleaseVersion string
	Operations     []AdvancedOperation
	patterns       map[string]*regexp.Regexp
}

// AdvancedOperation is one catalog entry, reduced to what dispatch needs.
type AdvancedOperation struct {
	Operation    string                `json:"operation"`
	Status       string                `json:"status"`
	Command      string                `json:"command"`
	Argv         []string              `json:"argv"`
	OptionalArgs []AdvancedOptionalArg `json:"optionalArgs"`
	Requires     AdvancedRequirements  `json:"requires"`
}

// AdvancedOptionalArg is one optional argv group; it is rendered only when
// every placeholder in it has a value.
type AdvancedOptionalArg struct {
	Argv []string `json:"argv"`
}

// AdvancedRequirements are the catalog's admission requirements.
type AdvancedRequirements struct {
	Capability          bool   `json:"capability"`
	CapabilityOperation string `json:"capabilityOperation"`
	TrustImported       bool   `json:"trustImported"`
	OwnerApproval       bool   `json:"ownerApproval"`
	CandidateSpec       bool   `json:"candidateSpec"`
	ChangeSet           bool   `json:"changeSet"`
}

type advancedOperationsDocument struct {
	SchemaVersion string              `json:"schemaVersion"`
	Program       string              `json:"program"`
	Placeholders  []advancedParameter `json:"placeholders"`
	Operations    []AdvancedOperation `json:"operations"`
}

type advancedParameter struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

// DecodeAdvancedOperations validates and decodes catalog bytes.
func DecodeAdvancedOperations(raw []byte, releaseVersion string) (AdvancedOperationsCatalog, error) {
	if len(raw) == 0 || len(raw) > maxAdvancedOperationsBytes {
		return AdvancedOperationsCatalog{}, fmt.Errorf("StackKits Advanced operations catalog must be non-empty and at most %d bytes", maxAdvancedOperationsBytes)
	}
	var document advancedOperationsDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return AdvancedOperationsCatalog{}, fmt.Errorf("decode StackKits Advanced operations catalog: %w", err)
	}
	if document.SchemaVersion != AdvancedOperationsSchemaVersion || document.Program != "stackkit" {
		return AdvancedOperationsCatalog{}, fmt.Errorf("StackKits Advanced operations catalog schema %q is not %s", document.SchemaVersion, AdvancedOperationsSchemaVersion)
	}
	patterns := make(map[string]*regexp.Regexp, len(document.Placeholders))
	for _, placeholder := range document.Placeholders {
		if placeholder.Pattern == "" {
			patterns[placeholder.Name] = nil
			continue
		}
		pattern, err := regexp.Compile(placeholder.Pattern)
		if err != nil {
			return AdvancedOperationsCatalog{}, fmt.Errorf("StackKits Advanced operations placeholder %s has an invalid pattern: %w", placeholder.Name, err)
		}
		patterns[placeholder.Name] = pattern
	}
	seen := map[string]bool{}
	for _, operation := range document.Operations {
		if operation.Operation == "" || seen[operation.Operation] {
			return AdvancedOperationsCatalog{}, fmt.Errorf("StackKits Advanced operations catalog has a missing or duplicate operation %q", operation.Operation)
		}
		seen[operation.Operation] = true
	}
	return AdvancedOperationsCatalog{
		SchemaVersion: document.SchemaVersion, ReleaseVersion: releaseVersion,
		Operations: document.Operations, patterns: patterns,
	}, nil
}

// AdvancedOperations returns the catalog this exact release shipped. A
// release without one fails with ErrAdvancedOperationsMissing.
func (release Release) AdvancedOperations() (AdvancedOperationsCatalog, error) {
	if len(release.advancedOperations) == 0 {
		return AdvancedOperationsCatalog{}, fmt.Errorf("%w (release %q)", ErrAdvancedOperationsMissing, release.receipt.Version)
	}
	return DecodeAdvancedOperations(release.advancedOperations, release.receipt.Version)
}

// readReleaseAdvancedOperations reads the catalog placed beside a release pin.
// A missing file leaves the release without a catalog; AdvancedOperations then
// refuses every Advanced dispatch.
func readReleaseAdvancedOperations(pinPath string) ([]byte, error) {
	if strings.TrimSpace(pinPath) == "" {
		return nil, nil
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(pinPath)), AdvancedOperationsFileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	raw, err := readBoundedRegularFile(path, maxAdvancedOperationsBytes)
	if err != nil {
		return nil, fmt.Errorf("read StackKits Advanced operations catalog: %w", err)
	}
	return raw, nil
}

// Lookup returns one catalog entry.
func (catalog AdvancedOperationsCatalog) Lookup(operation string) (AdvancedOperation, bool) {
	for _, entry := range catalog.Operations {
		if entry.Operation == operation {
			return entry, true
		}
	}
	return AdvancedOperation{}, false
}

// Dispatchable reports why an operation may not be dispatched to this
// release, or nil when it may. Only available entries with argv qualify.
func (catalog AdvancedOperationsCatalog) Dispatchable(operation string) error {
	entry, ok := catalog.Lookup(operation)
	if !ok {
		return fmt.Errorf("the pinned StackKits release %s does not list Advanced operation %s", catalog.ReleaseVersion, operation)
	}
	if entry.Status != advancedOperationStatusOK || len(entry.Argv) == 0 {
		return fmt.Errorf("the Advanced operation %s is %s, not available, in the pinned StackKits release %s", operation, entry.Status, catalog.ReleaseVersion)
	}
	return nil
}

// RenderArgv builds the argv words after the program name for one
// dispatchable operation. Every {placeholder} becomes exactly one argv word;
// a value the catalog constrains with a pattern must match it. Optional argv
// groups are rendered when every placeholder in them has a value.
func (catalog AdvancedOperationsCatalog) RenderArgv(operation string, values map[string]string) ([]string, error) {
	if err := catalog.Dispatchable(operation); err != nil {
		return nil, err
	}
	entry, _ := catalog.Lookup(operation)
	argv, err := catalog.renderWords(entry.Argv, values, true)
	if err != nil {
		return nil, fmt.Errorf("render Advanced operation %s: %w", operation, err)
	}
	for _, optional := range entry.OptionalArgs {
		words, optionalErr := catalog.renderWords(optional.Argv, values, false)
		if optionalErr != nil {
			return nil, fmt.Errorf("render Advanced operation %s: %w", operation, optionalErr)
		}
		argv = append(argv, words...)
	}
	return argv, nil
}

// renderWords renders one argv group. With required false a group whose
// placeholders lack values renders to nothing.
func (catalog AdvancedOperationsCatalog) renderWords(words []string, values map[string]string, required bool) ([]string, error) {
	out := make([]string, 0, len(words))
	for _, word := range words {
		match := advancedPlaceholderPattern.FindStringSubmatch(word)
		if match == nil {
			if strings.ContainsAny(word, "{}") {
				return nil, fmt.Errorf("argv word %q is not a literal or one placeholder", word)
			}
			out = append(out, word)
			continue
		}
		name := match[1]
		value, ok := values[name]
		if !ok || value == "" {
			if required {
				return nil, fmt.Errorf("placeholder {%s} has no value", name)
			}
			return nil, nil
		}
		if strings.ContainsRune(value, 0) || strings.HasPrefix(value, "-") {
			return nil, fmt.Errorf("placeholder {%s} value is not one argv word", name)
		}
		pattern, known := catalog.patterns[name]
		if !known {
			return nil, fmt.Errorf("placeholder {%s} is not declared by the catalog", name)
		}
		if pattern != nil && !pattern.MatchString(value) {
			return nil, fmt.Errorf("placeholder {%s} value does not match the catalog pattern", name)
		}
		out = append(out, value)
	}
	return out, nil
}
