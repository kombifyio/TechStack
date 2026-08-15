# Unifier Engine

## Purpose

The Unifier Engine is the intent-to-infrastructure bridge. It transforms `kombination.yaml` (user intent) through a 6-phase pipeline into executable IaC configurations via CUE validation, StackKit resolution, Add-On detection, worker placement, and OpenTofu generation.

## Core Features

- [x] Phase 1: IntentSpec parsing and pre-validation
- [x] Phase 2: RequirementsSpec generation (StackKit selection, Add-On detection, worker requirements)
- [x] Phase 3+4: Worker Registry integration and system-info collection via agents
- [ ] Phase 5: UnifiedSpec generation with placement engine (filter→score)
- [x] Phase 6: IaC generation (tfvars.json + HCL templates)
- [x] Pipeline API endpoints (POST /api/v1/unifier/pipeline)
- [ ] Spec persistence (RequirementsSpec + UnifiedSpec as YAML files)
- [ ] Full CUE Add-On merging (currently rule-based, target: CUE unification)

## Constraints

- MUST: Every spec passes CUE validation before execution
- MUST: IntentSpec is immutable — the system never modifies user intent
- MUST: All decisions are traceable (RequirementsSpec, UnifiedSpec persisted)
- MUST: UnifiedSpec format is StackKit-dependent (each StackKit defines its own CUE schema)
- MUST NOT: Contain hardware details in IntentSpec (IPs, worker assignments)
- MUST NOT: Skip CUE validation for any phase transition
- MUST NOT: Modify StackKit CUE schemas at runtime

## Success Criteria

- Given a valid kombination.yaml, the pipeline produces a valid UnifiedSpec
- Given an invalid kombination.yaml, the pipeline returns actionable CUE validation errors
- RequirementsSpec correctly identifies needed StackKit, Add-Ons, and hardware requirements
- UnifiedSpec placements match worker capabilities (filter→score algorithm)
- Generated IaC files (tfvars + HCL) are valid OpenTofu configurations

## Notes

- Architecture reference: docs/architecture/ARCHITECTURE_V2.md (Section 3)
- StackKits are consumed from external repo (kombify StackKits)
- Placement engine design: docs/concepts/Worker-Service-Matching-Algorithm.md
