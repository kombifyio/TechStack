# Domain Model (pkg/core)

## Purpose

Central domain types and interfaces that define the data model for the entire kombify-TechStack system. All other packages depend on these types.

## Core Features

- [x] Unifier interface (Analyze, Validate, Unify, Provision, Destroy, Status)
- [x] IntentSpec type (immutable user intent from kombination.yaml)
- [x] RequirementsSpec type (Phase 2 output: StackKit, workers, credentials, pre-checks)
- [x] UnifiedSpec type (resolved deployment plan with placements)
- [x] Worker/Node types (ResolvedNode, ServicePlacement)
- [ ] Remove deprecated types (StackConfig, NodeConfig, ServiceConfig, NetworkConfig)

## Constraints

- MUST: All spec types are defined here (single source of truth)
- MUST: IntentSpec contains only user intent (no hardware details)
- MUST: Types are serializable to JSON and YAML
- MUST NOT: Import any other pkg/ package (zero dependencies within project)
- MUST NOT: Contain business logic (only types and interfaces)

## Success Criteria

- All 24+ usages of deprecated types migrated to new spec types
- Zero circular dependencies with other packages
- All spec types have JSON/YAML struct tags
