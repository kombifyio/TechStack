package unifier

import "testing"

// A kit's own module import is dropped on purpose: the referenced package is
// concatenated into the same buffer rather than resolved through a module.
func TestInlinedKitModuleImportsAreNotTreatedAsUnresolvable(t *testing.T) {
	for _, spec := range []string{
		`import "github.com/kombifyio/stackkits/foundation"`,
		`import "github.com/kombifyio/stackkits/addons/ha"`,
		`import "foundation/generated"`,
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
