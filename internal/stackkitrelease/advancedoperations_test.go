package stackkitrelease

import (
	"errors"
	"os"
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
		"hostsCsv":          "app.kombify.me",
		"certificateFile":   ".stackkit/techstack-origin-certificate.pem",
	}
	placeholder := regexp.MustCompile(`^\{([A-Za-z0-9]+)\}$`)
	raw, err := os.ReadFile("testdata/advanced-operations-v0.50.0.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := DecodeAdvancedOperations(raw, "v0.50.0")
	if err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	for _, entry := range catalog.Operations {
		if entry.Status != "available" || strings.HasPrefix(entry.Operation, "application.") {
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
	}
}

// A release without its own catalog serves no Advanced operation: there is no
// embedded fallback catalog.
func TestReleaseWithoutCatalogRefusesAdvancedOperations(t *testing.T) {
	_, err := Release{receipt: Receipt{Version: "v0.50.0"}}.AdvancedOperations()
	if !errors.Is(err, ErrAdvancedOperationsMissing) {
		t.Fatalf("AdvancedOperations error = %v, want ErrAdvancedOperationsMissing", err)
	}
}
