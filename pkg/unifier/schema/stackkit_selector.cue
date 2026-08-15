// Package schema - StackKit selection helpers
// NOTE: This file must remain VALID CUE because it is embedded and compiled at runtime.
package schema

// NOTE: Embedded schemas are compiled file-by-file and then unified in Go.
// Cross-file references are therefore not available at compile-time.
// Keep this file self-contained.

// #StackKitRef is a reference to a StackKit template.
// Directory names use -kit suffix: basement-kit, cloud-kit, or modern-homelab.
#StackKitRef: "" | "basement-kit" | "cloud-kit" | "modern-homelab"

// #StackKitSelector is a small helper schema that can be used by tooling/preview.
// It is intentionally conservative: it only looks at fields that are present.
#StackKitSelector: {
	// Minimal input shape (raw user input may be empty)
	spec: {
		kit?: string
		nodes?: [...{
			provider?: string
			type?: string
		}]
	}

	// Derived values
	nodeCount: int | *0
	if spec.nodes != _|_ {
		nodeCount: len(spec.nodes)
	}

	hasDockerOnly: bool | *false
	if spec.nodes != _|_ {
		hasDockerOnly: nodeCount > 0 && len([for n in spec.nodes if n.provider == "docker" { n }]) == nodeCount
	}

	hasCloudProvider: bool | *false
	if spec.nodes != _|_ {
		hasCloudProvider: len([for n in spec.nodes if n.provider == "aws" || n.provider == "gcp" || n.provider == "azure" || n.provider == "hetzner" || n.provider == "digitalocean" || n.provider == "digitalocean-managed" || n.provider == "centron" || n.provider == "ionos" { n }]) > 0
	}

	// Output - default to basement-kit (canonical name)
	recommendedKit: #StackKitRef | *"basement-kit"
	if spec.kit != _|_ && spec.kit != "" {
		recommendedKit: spec.kit
	}
	if (spec.kit == _|_ || spec.kit == "") && hasDockerOnly {
		recommendedKit: "basement-kit"
	}
	if (spec.kit == _|_ || spec.kit == "") && hasCloudProvider {
		recommendedKit: "cloud-kit"
	}
}
