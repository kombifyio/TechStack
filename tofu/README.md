# OpenTofu Infrastructure Templates

This directory contains compatibility OpenTofu modules and blueprints consumed
by TechStack's Unifier pipeline.

The current product direction is StackKit-first:

- StackKit authoring lives in the separate `kombify-StackKits` repository.
- TechStack consumes StackKits through the loader path and converts the
  resulting spec chain into OpenTofu input.
- Files in `tofu/` are execution templates and local compatibility fixtures,
  not the canonical StackKit authoring surface.

## Architecture

The Unifier validates user intent, builds the requirements/unified spec chain,
and emits `terraform.tfvars.json` or equivalent OpenTofu inputs. These HCL
templates should stay dumb: they iterate over calculated resources and avoid
re-encoding product decisions already made in CUE/spec code.

See [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md) for the active pillar
model and [docs/CONFIGURATION.md](../docs/CONFIGURATION.md) for runtime
configuration.

## Directory Structure

```text
tofu/
├── modules/       # Reusable Terraform/OpenTofu modules
├── blueprints/    # Complete compatibility blueprints
└── examples/      # Example configurations, when present
```

## Module Reference

| Area     | Purpose                                                     |
| -------- | ----------------------------------------------------------- |
| compute  | VM/server resources and node shapes.                        |
| network  | Overlay, ingress, segmentation, and related network config. |
| storage  | Local, shared, or distributed storage inputs.               |
| identity | Auth and access-management infrastructure.                  |
| services | Service deployment primitives.                              |

Keep this directory aligned with the StackKit loader contract. Do not add new
product naming or wizard concepts here; extend the glossary and StackKit
schema docs instead.
