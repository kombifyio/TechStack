package unifier

import "testing"

func TestParseStackKitMetaYAMLV1Modes(t *testing.T) {
	meta, err := parseStackKitMetaYAML([]byte(`
apiVersion: stackkit/v1
kind: StackKit
metadata:
  name: basement-kit
  version: 1.0.0
modes:
  simple: {}
  advanced: {}
`))
	if err != nil {
		t.Fatalf("parseStackKitMetaYAML: %v", err)
	}
	if meta.Name != "basement-kit" || !meta.Mode.Simple || !meta.Mode.Advanced {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestParseStackKitMetaYAMLRejectsInvalidYAML(t *testing.T) {
	if _, err := parseStackKitMetaYAML([]byte(":\n-")); err == nil {
		t.Fatal("expected invalid YAML to fail")
	}
}
