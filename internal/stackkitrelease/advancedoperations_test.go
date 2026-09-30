package stackkitrelease

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestAdvancedArgvIsRenderedFromTheCatalogForEveryAvailableOperation(t *testing.T) {
	values := map[string]string{
		"capabilityFile":    ".stackkit/techstack-capability-1.json",
		"candidateSpecFile": ".stackkit/techstack-candidate-1.json",
		"changeSetId":       "sha256:" + strings.Repeat("a", 64),
		"changeSetSha256":   "sha256:" + strings.Repeat("b", 64),
		"bundleFile":        ".stackkit/techstack-advanced-trust-bundle.json",
		"bundleSha256":      "sha256:" + strings.Repeat("c", 64),
		"targetRef":         "sha256:" + strings.Repeat("d", 64),
	}
	placeholder := regexp.MustCompile(`^\{([A-Za-z0-9]+)\}$`)
	catalog, err := DecodeAdvancedOperations(pinnedAdvancedOperationsJSON, AdvancedOperationsSourceRelease, "v0.47.0")
	if err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	rendered := 0
	for _, entry := range catalog.Operations {
		if entry.Status != "available" {
			continue
		}
		argv, renderErr := catalog.RenderArgv(entry.Operation, values)
		if renderErr != nil {
			t.Fatalf("%s: %v", entry.Operation, renderErr)
		}
		want := make([]string, 0, len(entry.Argv))
		for _, word := range entry.Argv {
			if match := placeholder.FindStringSubmatch(word); match != nil {
				word = values[match[1]]
			}
			want = append(want, word)
		}
		if !slices.Equal(argv, want) {
			t.Fatalf("%s argv = %v, want the catalog argv %v", entry.Operation, argv, want)
		}
		rendered++
	}
	if rendered == 0 {
		t.Fatal("the catalog has no available operation")
	}
	// The embedded copy stands in for a release that predates the catalog, so
	// it refuses commands that ship after the pinned release.
	fallback, err := PinnedAdvancedOperations("v0.46.1")
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	if _, err := fallback.RenderArgv("restore.drill", values); err == nil {
		t.Fatal("the embedded catalog rendered restore.drill for a release before it shipped")
	}
}
