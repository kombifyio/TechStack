package unifier

import (
	"strings"
	"testing"
)

// StackKits' base/architecture_v2.cue imports "struct" and uses
// struct.MinFields(1) in four places. "struct" was missing from the loader's
// standard-library allowlist, so the import was silently dropped and every
// managed-runtime rollout failed to compile the combined buffer with four bare
// `reference "struct" not found` errors against synthetic line numbers.
func TestStructImportSurvivesExtraction(t *testing.T) {
	source := []byte(`package base

import (
	"list"
	"strings"
	"struct"
)

#ModuleNodeSelectionV2: {
	matchLabels?: {[string]: string} & struct.MinFields(1)
}
`)
	imports, body, dropped := extractImportsAndBody(source)
	if len(dropped) != 0 {
		t.Fatalf("dropped standard-library imports: %v", dropped)
	}
	joined := strings.Join(imports, " ")
	for _, want := range []string{`"struct"`, `"list"`, `"strings"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("import %s was dropped; imports = %v", want, imports)
		}
	}
	if !strings.Contains(body, "struct.MinFields(1)") {
		t.Fatalf("body lost the struct usage:\n%s", body)
	}
	if strings.Contains(body, "import (") || strings.Contains(body, "package ") {
		t.Fatalf("body still carries package/import declarations:\n%s", body)
	}
}

// The allowlist must stay complete. Anything CUE ships that a kit might import
// has to round-trip, or it fails the same silent way "struct" did.
func TestEveryCUEStdLibImportRoundTrips(t *testing.T) {
	for pkg := range cueStdLibPackages {
		t.Run(pkg, func(t *testing.T) {
			source := []byte("package p\n\nimport (\n\t\"" + pkg + "\"\n)\n\n#X: 1\n")
			imports, _, dropped := extractImportsAndBody(source)
			if len(dropped) != 0 {
				t.Fatalf("%q was dropped", pkg)
			}
			if len(imports) != 1 || !strings.Contains(imports[0], `"`+pkg+`"`) {
				t.Fatalf("imports = %v, want the %q spec", imports, pkg)
			}
		})
	}
}

// A kit's own module import is dropped on purpose: the referenced package is
// concatenated into the same buffer rather than resolved through a module.
func TestInlinedKitModuleImportsAreNotTreatedAsUnresolvable(t *testing.T) {
	for _, spec := range []string{
		`import "github.com/kombifyio/stackkits/base"`,
		`import "github.com/kombihq/stackkits/base"`,
		`import "github.com/kombifyio/stackkits/addons/ha"`,
		`import "base/generated"`,
	} {
		t.Run(spec, func(t *testing.T) {
			_, _, dropped := extractImportsAndBody([]byte("package p\n\n" + spec + "\n\n#X: 1\n"))
			if len(dropped) != 1 {
				t.Fatalf("dropped = %v, want exactly the module import", dropped)
			}
			if unresolvable := unresolvableDroppedImports(dropped); len(unresolvable) != 0 {
				t.Fatalf("inlined module import reported as unresolvable: %v", unresolvable)
			}
		})
	}
}

// Anything else must fail loudly at load time. Silently dropping it is what
// turned one missing allowlist entry into four unexplained reference errors in
// a live rollout.
func TestUnknownImportIsReportedAsUnresolvable(t *testing.T) {
	_, _, dropped := extractImportsAndBody(
		[]byte("package p\n\nimport (\n\t\"example.com/other/pkg\"\n)\n\n#X: 1\n"))
	unresolvable := unresolvableDroppedImports(dropped)
	if len(unresolvable) != 1 || unresolvable[0] != "example.com/other/pkg" {
		t.Fatalf("unresolvable = %v, want the unknown import", unresolvable)
	}
}

// Aliased and commented import specs must not confuse the classifier.
func TestImportBlockTolerance(t *testing.T) {
	source := []byte(`package p

import (
	// a comment
	l "list"

	"struct"
)

#X: l.MinItems(1) & struct.MinFields(1)
`)
	imports, _, dropped := extractImportsAndBody(source)
	if len(dropped) != 0 {
		t.Fatalf("dropped = %v", dropped)
	}
	if len(imports) != 2 {
		t.Fatalf("imports = %v, want the aliased list plus struct", imports)
	}
}
